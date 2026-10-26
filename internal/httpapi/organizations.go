package httpapi

import (
	"sort"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/audit"
	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
	"github.com/Dvorinka/tock/internal/importer"
)

// ---- shared helpers ----

// orgScope resolves the {organization} route param, the caller's membership and
// the permission set for that membership. It writes the error response and
// returns false on failure — mirroring Laravel model binding (404) +
// checkPermission (403).
type orgScope struct {
	org    db.Organization
	member db.Member
	perms  map[string]bool
}

func (o *orgScope) has(perm string) bool { return o.perms[perm] }

func (s *Server) resolveOrg(w http.ResponseWriter, r *http.Request, perm string) (*orgScope, bool) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeMessage(w, http.StatusUnauthorized, "Unauthenticated.")
		return nil, false
	}
	orgID, ok := domain.UUID(chi.URLParam(r, "organization"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Organization", chi.URLParam(r, "organization")))
		return nil, false
	}
	q := db.New(s.pool)
	org, err := q.GetOrganization(r.Context(), orgID)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Organization", chi.URLParam(r, "organization")))
		return nil, false
	}
	uid, _ := domain.UUID(u.ID)
	member, err := q.GetMember(r.Context(), db.GetMemberParams{UserID: uid, OrganizationID: orgID})
	perms := map[string]bool{}
	if err == nil {
		for _, p := range auth.PermissionsForRole(deref(member.Role), org.EmployeesCanManageTasks) {
			perms[p] = true
		}
	}
	if perm != "" && !perms[perm] {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return nil, false
	}
	return &orgScope{org: org, member: member, perms: perms}, true
}

// requireScopedMember loads {member} and verifies it belongs to the org —
// Laravel route binding + MemberController::checkPermission.
func (s *Server) scopedMember(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.Member, bool) {
	var member db.Member
	id, ok := domain.UUID(chi.URLParam(r, "member"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Member", chi.URLParam(r, "member")))
		return member, false
	}
	member, err := db.New(s.pool).GetMemberByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Member", chi.URLParam(r, "member")))
		return member, false
	}
	if member.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return member, false
	}
	return member, true
}

func (s *Server) scopedInvitation(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.OrganizationInvitation, bool) {
	var inv db.OrganizationInvitation
	id, ok := domain.UUID(chi.URLParam(r, "invitation"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("OrganizationInvitation", chi.URLParam(r, "invitation")))
		return inv, false
	}
	inv, err := db.New(s.pool).GetInvitation(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("OrganizationInvitation", chi.URLParam(r, "invitation")))
		return inv, false
	}
	if inv.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return inv, false
	}
	return inv, true
}

func modelNotFound(model, id string) string {
	return "No query results for model [App\\Models\\" + model + "] " + id
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// writeDomainErr renders domain.APIError like upstream's ApiException
// ({error: true, key, message} @400); anything else is a 500.
func writeDomainErr(w http.ResponseWriter, err error) {
	var ae *domain.APIError
	if errors.As(err, &ae) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": true, "key": ae.Key, "message": ae.Message,
		})
		return
	}
	writeMessage(w, http.StatusInternalServerError, "server error")
}

// validationErr renders Laravel's ValidationException shape.
func validationErr(w http.ResponseWriter, errs map[string][]string) {
	// Laravel ValidationException puts the first bag message in `message`
	// (notification toasts read it directly); keys sorted for determinism.
	msg := "The given data was invalid."
	keys := make([]string, 0, len(errs))
	for k := range errs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(errs[k]) > 0 {
			msg = errs[k][0]
			break
		}
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"message": msg, "errors": errs,
	})
}

func decodeJSONMap(r *http.Request) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		return nil, false
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	return m, true
}

func jsonStr(m map[string]json.RawMessage, key string) (string, bool, bool) {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return "", false, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true, false
	}
	return s, true, true
}

// jsonBool reads an optional bool field (Laravel 'boolean' accepts true/false/1/0/"1"/"0").
func jsonBool(m map[string]json.RawMessage, key string) (bool, bool, bool) {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return false, false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true, true
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil && (n == 0 || n == 1) {
		return n == 1, true, true
	}
	return false, true, false
}

// jsonNullableInt reads an optional nullable int field (Laravel 'integer','min:0','max:2147483647').
// Returns (value, present, valid); value is nil for JSON null.
func jsonNullableInt(m map[string]json.RawMessage, key string) (*int32, bool, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false, false
	}
	if string(raw) == "null" {
		return nil, true, true
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f != float64(int64(f)) || f < 0 || f > 2147483647 {
		return nil, true, false
	}
	v := int32(f)
	return &v, true, true
}

// ---- organizations ----

func organizationResource(org db.Organization, showBillableRate bool) map[string]any {
	var rate any
	if showBillableRate && org.BillableRate != nil {
		rate = *org.BillableRate
	}
	return map[string]any{
		"id":                               domain.UUIDString(org.ID),
		"name":                             org.Name,
		"is_personal":                      org.PersonalTeam,
		"billable_rate":                    rate,
		"can_use_billable_rates":           true,
		"employees_can_see_billable_rates": org.EmployeesCanSeeBillableRates,
		"employees_can_manage_tasks":       org.EmployeesCanManageTasks,
		"prevent_overlapping_time_entries": org.PreventOverlappingTimeEntries,
		"breaks_enabled":                   org.BreaksEnabled,
		"currency":                         org.Currency,
		"currency_symbol":                  domain.CurrencySymbol(org.Currency),
		"number_format":                    org.NumberFormat,
		"currency_format":                  org.CurrencyFormat,
		"date_format":                      org.DateFormat,
		"interval_format":                  org.IntervalFormat,
		"time_format":                      org.TimeFormat,
	}
}

func (s *Server) handleShowOrganization(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "organizations:view")
	if !ok {
		return
	}
	// BillableRateService::canSeeBillableRates — employees need the org flag.
	show := deref(scope.member.Role) != domain.RoleEmployee || scope.org.EmployeesCanSeeBillableRates
	writeJSON(w, http.StatusOK, map[string]any{"data": organizationResource(scope.org, show)})
}

var numberFormats = map[string]bool{
	"point-comma": true, "comma-point": true, "space-comma": true,
	"space-point": true, "apostrophe-point": true,
}
var currencyFormats = map[string]bool{
	"iso-code-before-with-space": true, "iso-code-after-with-space": true,
	"symbol-before": true, "symbol-after": true,
	"symbol-before-with-space": true, "symbol-after-with-space": true,
}
var dateFormats = map[string]bool{
	"point-separated-d-m-yyyy": true, "slash-separated-mm-dd-yyyy": true,
	"slash-separated-dd-mm-yyyy": true, "hyphen-separated-dd-mm-yyyy": true,
	"hyphen-separated-mm-dd-yyyy": true, "hyphen-separated-yyyy-mm-dd": true,
}
var intervalFormats = map[string]bool{
	"decimal": true, "hours-minutes": true,
	"hours-minutes-colon-separated": true, "hours-minutes-seconds-colon-separated": true,
}
var timeFormats = map[string]bool{"12-hours": true, "24-hours": true}

func (s *Server) handleUpdateOrganization(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "organizations:update")
	if !ok {
		return
	}
	org := scope.org
	auditBefore := organizationAttrs(scope.org)
	body, ok := decodeJSONMap(r)
	if !ok {
		validationErr(w, map[string][]string{"name": {"The given data was invalid."}})
		return
	}
	errs := map[string][]string{}
	for _, f := range []struct {
		key  string
		set  func(string)
		enum map[string]bool
	}{
		{"name", func(v string) { org.Name = v }, nil},
		{"number_format", func(v string) { org.NumberFormat = v }, numberFormats},
		{"currency_format", func(v string) { org.CurrencyFormat = v }, currencyFormats},
		{"date_format", func(v string) { org.DateFormat = v }, dateFormats},
		{"interval_format", func(v string) { org.IntervalFormat = v }, intervalFormats},
		{"time_format", func(v string) { org.TimeFormat = v }, timeFormats},
	} {
		if v, present, valid := jsonStr(body, f.key); present {
			if !valid || (f.enum != nil && !f.enum[v]) || (f.key == "name" && (v == "" || len(v) > 255)) {
				errs[f.key] = []string{"The selected " + strings.ReplaceAll(f.key, "_", " ") + " is invalid."}
				continue
			}
			f.set(v)
		}
	}
	if v, present, valid := jsonStr(body, "currency"); present {
		if !valid || !domain.ValidCurrency(v) {
			errs["currency"] = []string{"The currency field must be a valid currency code (ISO 4217)."}
		} else {
			org.Currency = v
		}
	}
	for _, f := range []struct {
		key string
		set func(bool)
	}{
		{"employees_can_see_billable_rates", func(v bool) { org.EmployeesCanSeeBillableRates = v }},
		{"employees_can_manage_tasks", func(v bool) { org.EmployeesCanManageTasks = v }},
		{"prevent_overlapping_time_entries", func(v bool) { org.PreventOverlappingTimeEntries = v }},
		{"breaks_enabled", func(v bool) { org.BreaksEnabled = v }},
	} {
		if v, present, valid := jsonBool(body, f.key); present {
			if !valid {
				errs[f.key] = []string{"The " + strings.ReplaceAll(f.key, "_", " ") + " field must be true or false."}
				continue
			}
			// canUseBillableRates is always true (OSS BillingContract) — set directly.
			f.set(v)
		}
	}
	oldRate := org.BillableRate
	if rate, present, valid := jsonNullableInt(body, "billable_rate"); present {
		if !valid {
			errs["billable_rate"] = []string{"The billable rate field must be an integer."}
		} else if rate != nil && *rate == 0 {
			org.BillableRate = nil // upstream maps 0 to null
		} else {
			org.BillableRate = rate
		}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	q := db.New(s.pool)
	org, err := q.UpdateOrganization(r.Context(), db.UpdateOrganizationParams{
		ID:                            org.ID,
		Name:                          org.Name,
		Currency:                      org.Currency,
		BillableRate:                  org.BillableRate,
		EmployeesCanSeeBillableRates:  org.EmployeesCanSeeBillableRates,
		EmployeesCanManageTasks:       org.EmployeesCanManageTasks,
		PreventOverlappingTimeEntries: org.PreventOverlappingTimeEntries,
		BreaksEnabled:                 org.BreaksEnabled,
		NumberFormat:                  org.NumberFormat,
		CurrencyFormat:                org.CurrencyFormat,
		DateFormat:                    org.DateFormat,
		IntervalFormat:                org.IntervalFormat,
		TimeFormat:                    org.TimeFormat,
		UserID:                        org.UserID,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if (oldRate == nil) != (org.BillableRate == nil) ||
		(oldRate != nil && org.BillableRate != nil && *oldRate != *org.BillableRate) {
		_ = q.PropagateBillableRateForOrganization(r.Context(), db.PropagateBillableRateForOrganizationParams{
			OrganizationID: org.ID, BillableRate: org.BillableRate,
		})
	}
	s.recordAudit(r, audit.EventUpdated, audit.TypeOrganization, org.ID, scope.org.ID, pgtype.UUID{}, auditBefore, organizationAttrs(org))
	writeJSON(w, http.StatusOK, map[string]any{"data": organizationResource(org, true)})
}

func (s *Server) handleStoreOrganization(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || len(body.Name) > 255 {
		errs := map[string][]string{}
		if body.Name == "" {
			errs["name"] = []string{"The name field is required."}
		} else {
			errs["name"] = []string{"The name field must not be greater than 255 characters."}
		}
		validationErr(w, errs)
		return
	}
	uid, _ := domain.UUID(u.ID)
	// IpLookupService is the no-op stub upstream — currency is always the
	// config default (EUR).
	org, err := s.domain.CreateOrganization(r.Context(), body.Name, uid, false, "")
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	q := db.New(s.pool)
	_ = s.domain.SwitchCurrentOrganization(r.Context(), q, uid, org.ID)
	// Organization audits are owned by the organization itself.
	s.recordAudit(r, audit.EventCreated, audit.TypeOrganization, org.ID, org.ID, pgtype.UUID{}, nil, organizationAttrs(org))
	writeJSON(w, http.StatusCreated, map[string]any{"data": organizationResource(org, true)})
}

func (s *Server) handleDestroyOrganization(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	scope, ok := s.resolveOrg(w, r, "organizations:delete")
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Password == "" {
		validationErr(w, map[string][]string{"password": {"The password field is required."}})
		return
	}
	if u.Password == nil || !auth.CheckPassword(*u.Password, body.Password) {
		validationErr(w, map[string][]string{"password": {"The password is incorrect."}})
		return
	}
	if err := s.domain.DeleteOrganization(r.Context(), scope.org.ID, pgtype.UUID{}); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- pagination (Laravel LengthAwarePaginator shape) ----

const defaultPerPage = 15 // config('app.pagination_per_page_default')

func pageParam(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	return p
}

func paginateURL(r *http.Request, page int) string {
	q := r.URL.Query()
	q.Set("page", strconv.Itoa(page))
	return r.URL.Path + "?" + q.Encode()
}

// laravelPageLinks reproduces LengthAwarePaginator's meta.links window
// (onEachSide=3): Previous, sliding page numbers, Next.
func laravelPageLinks(r *http.Request, page, last int) []map[string]any {
	links := []map[string]any{}
	var prev any
	if page > 1 {
		prev = paginateURL(r, page-1)
	}
	links = append(links, map[string]any{"url": prev, "label": "&laquo; Previous", "active": false})
	pages := laravelPageWindow(page, last)
	for _, p := range pages {
		if p < 0 {
			links = append(links, map[string]any{"url": nil, "label": "...", "active": false})
			continue
		}
		links = append(links, map[string]any{
			"url": paginateURL(r, p), "label": strconv.Itoa(p), "active": p == page,
		})
	}
	var next any
	if page < last {
		next = paginateURL(r, page+1)
	}
	links = append(links, map[string]any{"url": next, "label": "Next &raquo;", "active": false})
	return links
}

// laravelPageWindow mirrors UrlWindow::get: small ranges list everything,
// larger ones show a slider of 3 around the current page plus first/last.
func laravelPageWindow(current, last int) []int {
	if last <= 11 {
		out := make([]int, last)
		for i := range out {
			out[i] = i + 1
		}
		return out
	}
	window := map[int]bool{1: true, last: true}
	for d := -3; d <= 3; d++ {
		if p := current + d; p >= 1 && p <= last {
			window[p] = true
		}
	}
	pages := make([]int, 0, len(window))
	for p := 1; p <= last; p++ {
		if window[p] {
			pages = append(pages, p)
		}
	}
	out := []int{}
	for i, p := range pages {
		if i > 0 && p != pages[i-1]+1 {
			out = append(out, -1)
		}
		out = append(out, p)
	}
	return out
}

// paginatedResponse writes {data, links, meta} like a Laravel ResourceCollection.
func paginatedResponse(w http.ResponseWriter, r *http.Request, data any, total int64, page, perPage int) {
	last := int((total + int64(perPage) - 1) / int64(perPage))
	if last < 1 {
		last = 1
	}
	var from, to any
	n, _ := dataLen(data)
	if n > 0 {
		from = (page-1)*perPage + 1
		to = (page-1)*perPage + n
	}
	base := r.URL.Path
	var firstURL, lastURL any
	firstURL = paginateURL(r, 1)
	lastURL = paginateURL(r, last)
	var prev, next any
	if page > 1 {
		prev = paginateURL(r, page-1)
	}
	if page < last {
		next = paginateURL(r, page+1)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": data,
		"links": map[string]any{
			"first": firstURL, "last": lastURL, "prev": prev, "next": next,
		},
		"meta": map[string]any{
			"current_page": page,
			"from":         from,
			"last_page":    last,
			"links":        laravelPageLinks(r, page, last),
			"path":         base,
			"per_page":     perPage,
			"to":           to,
			"total":        total,
		},
	})
}

func dataLen(v any) (int, bool) {
	switch d := v.(type) {
	case []map[string]any:
		return len(d), true
	case []any:
		return len(d), true
	}
	return 0, false
}

// ---- members ----

func memberResource(row db.ListMembersPaginatedRow) map[string]any {
	var rate any
	if row.BillableRate != nil {
		rate = *row.BillableRate
	}
	return map[string]any{
		"id":             domain.UUIDString(row.ID),
		"user_id":        domain.UUIDString(row.UserID),
		"name":           row.UserName,
		"email":          row.UserEmail,
		"role":           deref(row.Role),
		"is_placeholder": row.UserIsPlaceholder,
		"billable_rate":  rate,
	}
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "members:view")
	if !ok {
		return
	}
	page := pageParam(r)
	q := db.New(s.pool)
	total, err := q.CountMembers(r.Context(), scope.org.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rows, err := q.ListMembersPaginated(r.Context(), db.ListMembersPaginatedParams{
		OrganizationID: scope.org.ID, Limit: defaultPerPage, Offset: int32((page - 1) * defaultPerPage),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := []map[string]any{}
	for _, row := range rows {
		data = append(data, memberResource(row))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

var validRoles = map[string]bool{
	domain.RoleOwner: true, domain.RoleAdmin: true, domain.RoleManager: true,
	domain.RoleEmployee: true, domain.RolePlaceholder: true,
}

func (s *Server) handleUpdateMember(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "members:update")
	if !ok {
		return
	}
	member, ok := s.scopedMember(w, r, scope)
	if !ok {
		return
	}
	auditBefore := memberAttrs(member)
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid body")
		return
	}
	errs := map[string][]string{}
	var newRole string
	if v, present, valid := jsonStr(body, "role"); present {
		if !valid || !validRoles[v] {
			errs["role"] = []string{"The selected role is invalid."}
		} else {
			newRole = v
		}
	}
	oldRate := member.BillableRate
	var rate *int32
	var rateChanged bool
	if v, present, valid := jsonNullableInt(body, "billable_rate"); present {
		if !valid {
			errs["billable_rate"] = []string{"The billable rate field must be an integer."}
		} else {
			rate = v
			rateChanged = true
			if v != nil && *v == 0 {
				rate = nil
			}
		}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	q := db.New(s.pool)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qt := q.WithTx(tx)
	if rateChanged {
		member.BillableRate = rate
		if err := qt.UpdateMemberBillableRate(r.Context(), db.UpdateMemberBillableRateParams{ID: member.ID, BillableRate: rate}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if newRole != "" && newRole != deref(member.Role) {
		if err := s.domain.ChangeRole(r.Context(), qt, &member, scope.org.ID, newRole, scope.has("members:change-ownership")); err != nil {
			writeDomainErr(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if (oldRate == nil) != (member.BillableRate == nil) ||
		(oldRate != nil && member.BillableRate != nil && *oldRate != *member.BillableRate) {
		_ = q.PropagateBillableRateForMember(r.Context(), db.PropagateBillableRateForMemberParams{
			OrganizationID: scope.org.ID, MemberID: member.ID, BillableRate: member.BillableRate,
		})
	}
	name, email, isPlaceholder := s.memberUserInfo(r, member.UserID)
	out := memberResource(db.ListMembersPaginatedRow{
		ID: member.ID, UserID: member.UserID, Role: member.Role,
		BillableRate: member.BillableRate, UserName: name, UserEmail: email,
		UserIsPlaceholder: isPlaceholder,
	})
	s.recordAudit(r, audit.EventUpdated, audit.TypeMember, member.ID, scope.org.ID, pgtype.UUID{}, auditBefore, memberAttrs(member))
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) memberUserInfo(r *http.Request, userID pgtype.UUID) (string, string, bool) {
	var name, email string
	var ph bool
	_ = s.pool.QueryRow(r.Context(),
		`SELECT name, email, is_placeholder FROM users WHERE id = $1`, userID).Scan(&name, &email, &ph)
	return name, email, ph
}

func (s *Server) handleDestroyMember(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "members:delete")
	if !ok {
		return
	}
	member, ok := s.scopedMember(w, r, scope)
	if !ok {
		return
	}
	dr := r.URL.Query().Get("delete_related")
	if dr != "" && dr != "true" && dr != "false" {
		validationErr(w, map[string][]string{"delete_related": {"The selected delete related is invalid."}})
		return
	}
	var isPlaceholder bool
	_ = s.pool.QueryRow(r.Context(),
		`SELECT is_placeholder FROM users WHERE id = $1`, member.UserID).Scan(&isPlaceholder)
	if err := s.domain.RemoveMember(r.Context(), member, isPlaceholder, dr == "true"); err != nil {
		writeDomainErr(w, err)
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeMember, member.ID, scope.org.ID, pgtype.UUID{}, memberAttrs(member), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMakePlaceholder(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "members:make-placeholder")
	if !ok {
		return
	}
	member, ok := s.scopedMember(w, r, scope)
	if !ok {
		return
	}
	switch deref(member.Role) {
	case domain.RoleOwner:
		writeDomainErr(w, domain.ErrCanNotRemoveOwner)
		return
	case domain.RolePlaceholder:
		writeDomainErr(w, domain.ErrChangingRoleOfPlaceholder)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if err := s.domain.MakeMemberToPlaceholder(r.Context(), db.New(s.pool).WithTx(tx), &member, true); err != nil {
		writeDomainErr(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInvitePlaceholder(w http.ResponseWriter, r *http.Request) {
	u := apiUser(w, r)
	if u == nil {
		return
	}
	scope, ok := s.resolveOrg(w, r, "members:invite-placeholder")
	if !ok {
		return
	}
	member, ok := s.scopedMember(w, r, scope)
	if !ok {
		return
	}
	var isPlaceholder bool
	var email string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT is_placeholder, email FROM users WHERE id = $1`, member.UserID).Scan(&isPlaceholder, &email); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if !isPlaceholder {
		writeDomainErr(w, domain.ErrUserNotPlaceholder)
		return
	}
	if strings.HasSuffix(email, "@solidtime-import.test") {
		writeDomainErr(w, domain.ErrPlaceholderNotInvitable)
		return
	}
	q := db.New(s.pool)
	inv, err := s.domain.InviteUser(r.Context(), q, scope.org.ID, email, domain.RoleEmployee)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	s.sendInvitationMail(r, inv, scope.org)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMergeInto(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "members:merge-into")
	if !ok {
		return
	}
	member, ok := s.scopedMember(w, r, scope)
	if !ok {
		return
	}
	var isPlaceholder bool
	_ = s.pool.QueryRow(r.Context(),
		`SELECT is_placeholder FROM users WHERE id = $1`, member.UserID).Scan(&isPlaceholder)
	if deref(member.Role) != domain.RolePlaceholder || !isPlaceholder {
		writeDomainErr(w, domain.ErrOnlyPlaceholdersCanBeMerged)
		return
	}
	var body struct {
		MemberID string `json:"member_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	toID, valid := domain.UUID(body.MemberID)
	var to db.Member
	err := error(nil)
	if valid {
		to, err = db.New(s.pool).GetMemberByID(r.Context(), toID)
	}
	if !valid || err != nil || to.OrganizationID != scope.org.ID {
		validationErr(w, map[string][]string{"member_id": {"The selected member id is invalid."}})
		return
	}
	if err := s.domain.MergePlaceholderInto(r.Context(), member, to); err != nil {
		writeDomainErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- invitations ----

func invitationResource(inv db.OrganizationInvitation) map[string]any {
	return map[string]any{
		"id":    domain.UUIDString(inv.ID),
		"email": inv.Email,
		"role":  deref(inv.Role),
	}
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invitations:view")
	if !ok {
		return
	}
	page := pageParam(r)
	q := db.New(s.pool)
	total, err := q.CountInvitations(r.Context(), scope.org.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rows, err := q.ListInvitationsPaginated(r.Context(), db.ListInvitationsPaginatedParams{
		OrganizationID: scope.org.ID, Limit: defaultPerPage, Offset: int32((page - 1) * defaultPerPage),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := []map[string]any{}
	for _, inv := range rows {
		data = append(data, invitationResource(inv))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

func (s *Server) handleStoreInvitation(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invitations:create")
	if !ok {
		return
	}
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	errs := map[string][]string{}
	if body.Email == "" {
		errs["email"] = []string{"The email field is required."}
	} else if !strings.Contains(body.Email, "@") {
		errs["email"] = []string{"The email field must be a valid email address."}
	}
	switch body.Role {
	case "":
		errs["role"] = []string{"The role field is required."}
	case domain.RoleAdmin, domain.RoleManager, domain.RoleEmployee:
	default:
		errs["role"] = []string{"The selected role is invalid."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	inv, err := s.domain.InviteUser(r.Context(), db.New(s.pool), scope.org.ID, body.Email, body.Role)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	s.sendInvitationMail(r, inv, scope.org)
	s.recordAudit(r, audit.EventCreated, audit.TypeOrganizationInvitation, inv.ID, scope.org.ID, pgtype.UUID{}, nil, invitationAttrs(inv))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResendInvitation(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invitations:resend")
	if !ok {
		return
	}
	inv, ok := s.scopedInvitation(w, r, scope)
	if !ok {
		return
	}
	s.sendInvitationMail(r, inv, scope.org)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDestroyInvitation(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "invitations:remove")
	if !ok {
		return
	}
	inv, ok := s.scopedInvitation(w, r, scope)
	if !ok {
		return
	}
	if err := db.New(s.pool).DeleteInvitation(r.Context(), inv.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeOrganizationInvitation, inv.ID, scope.org.ID, pgtype.UUID{}, invitationAttrs(inv), nil)
	w.WriteHeader(http.StatusNoContent)
}

// signInvitationURL builds the Laravel signed relative URL wrapped in the app
// URL — matching URL::to(URL::signedRoute(..., absolute: false)) with a 90-day
// expiry like OrganizationInvitationMail.
func (s *Server) signInvitationURL(invitationID string) string {
	path := "/organization-invitations/" + invitationID
	q := "expires=" + strconv.FormatInt(time.Now().Add(90*24*time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(s.appKey))
	mac.Write([]byte(path + "?" + q))
	return s.appURL + path + "?" + q + "&signature=" + hex.EncodeToString(mac.Sum(nil))
}

// verifySignedURL ports Illuminate ValidateSignature. relative=false matches
// middleware 'signed' (absolute URL signed); relative=true 'signed:relative'.
func (s *Server) verifyURLSignature(r *http.Request, relative bool) bool {
	expires, _ := strconv.ParseInt(r.URL.Query().Get("expires"), 10, 64)
	if expires > 0 && time.Now().Unix() > expires {
		return false
	}
	sig := r.URL.Query().Get("signature")
	if sig == "" {
		return false
	}
	parts := strings.Split(r.URL.RawQuery, "&")
	kept := parts[:0]
	for _, p := range parts {
		if !strings.HasPrefix(p, "signature=") {
			kept = append(kept, p)
		}
	}
	qs := strings.Join(kept, "&")
	var original string
	if relative {
		original = r.URL.Path + "?" + qs
	} else {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		original = scheme + "://" + r.Host + r.URL.Path + "?" + qs
	}
	mac := hmac.New(sha256.New, []byte(s.appKey))
	mac.Write([]byte(original))
	return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))
}

func (s *Server) sendInvitationMail(r *http.Request, inv db.OrganizationInvitation, org db.Organization) {
	url := s.signInvitationURL(domain.UUIDString(inv.ID))
	body := "You have been invited to join the " + org.Name + " organization!\n\n" +
		"Accept Invitation: " + url + "\n\n" +
		"If you did not expect to receive an invitation to this organization, you may discard this email."
	_ = s.sendMail(r.Context(), inv.Email, "Organization Invitation", body)
}

// handleAcceptInvitation ports Web\OrganizationInvitationController::accept.
// relative=true matches 'signed:relative'; false matches legacy 'signed'.
func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request, relative bool) {
	if !s.verifyURLSignature(r, relative) {
		http.Error(w, "Invalid signature.", http.StatusForbidden)
		return
	}
	id, ok := domain.UUID(chi.URLParam(r, "invitation"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := db.New(s.pool)
	inv, err := q.GetInvitation(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	org, err := q.GetOrganization(r.Context(), inv.OrganizationID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	role := deref(inv.Role)
	if role != domain.RoleAdmin && role != domain.RoleManager && role != domain.RoleEmployee {
		http.Error(w, "Invalid role", http.StatusInternalServerError)
		return
	}
	email := strings.ToLower(inv.Email)
	sess := auth.SessionFromContext(r.Context())
	flash := func(text, style string) {
		if sess != nil {
			sess.Set("bannerText", text)
			sess.Set("bannerStyle", style)
		}
	}
	var inviteeID pgtype.UUID
	var isPlaceholder bool
	err = s.pool.QueryRow(r.Context(),
		`SELECT id, is_placeholder FROM users WHERE lower(email) = lower($1) AND is_placeholder = false`,
		email).Scan(&inviteeID, &isPlaceholder)
	if errors.Is(err, pgx.ErrNoRows) {
		if !inv.AcceptedAt.Valid {
			_ = q.MarkInvitationAccepted(r.Context(), inv.ID)
		}
		if sess != nil {
			sess.Set("registration_email", email)
		}
		flash("Please create an account to finish joining the "+org.Name+" organization.", "info")
		Redirect(w, r, "/register")
		return
	} else if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	alreadyMember, _ := s.domain.IsEmailAlreadyMember(r.Context(), q, org.ID, email)
	if !alreadyMember {
		if _, err := s.domain.AddMember(r.Context(), inviteeID, org.ID, role); err == nil {
			_ = q.DeleteInvitation(r.Context(), inv.ID)
		}
	}
	if auth.UserFromContext(r.Context()) == nil {
		flash("Great! You have accepted the invitation to join the "+org.Name+" organization. Please log in to access it.", "success")
		Redirect(w, r, "/login")
		return
	}
	if alreadyMember {
		flash("You are already a member of the "+org.Name+" organization.", "danger")
	} else {
		flash("Great! You have accepted the invitation to join the "+org.Name+" organization.", "success")
	}
	Redirect(w, r, "/dashboard")
}

// ---- importers ----

// importerInfo mirrors ImporterProvider + lang/en/importer.php verbatim.
var importerInfo = []map[string]string{
	{"key": "toggl_time_entries", "name": "Toggl Time Entries", "description": `<strong>Important:</strong> If you want to import a Toggl organization use the "Toggl Data Importer" before using this importer, since this export contains more details. <br><br>1. Go to Admin -> Settings -> Data export. <br>2. Under "Time entries" select the year you want to export and click on "Export time entries". <br><br>You can export all years one after another and import them one after another.  <br>Before you import make sure that the Timezone settings in Toggl are the same as in solidtime.`},
	{"key": "toggl_data_importer", "name": "Toggl Data Importer", "description": `1. Go to Admin -> Settings -> Data export. <br>2. Under "Data Export" select all items for export and click on "Export to email". <br> 3. You will receive an email with a download link. Download the ZIP and upload it here. <br><br>The "Data Export" exports everything except time entries. If you want to also import time entries use the "Toggl Time Entries" importer afterwards.`},
	{"key": "clockify_time_entries", "name": "Clockify Time Entries", "description": `<strong>Important:</strong> If you also want to import your projects use the "Clockify Projects" importer before this one, since that export contains more details such as billable status, billable rates and estimated time.<br><br>1. First make sure that you set the Date format to "MM/DD/YYYY" and the Time format to "12-hour" in the user settings.<br>2. In the same preferences page change the language of Clockfiy to English.<br>3. Go to REPORTS -> TIME -> Detailed in the navigation on the left. <br>4. Now select the date range that you want to export in the right top. In the free Clockify plan it's currently not possible to select more than one year. You can export each year separately and import them one after another.<br> 4. Now click Export -> Save as CSV. The Export dropdown is in the header of the export table left of the printer symbol. <br><br>Before you import make sure that the Timezone settings in Clockify are the same as in solidtime.`},
	{"key": "clockify_projects", "name": "Clockify Projects", "description": `1. Make sure to set the language of Clockify to English in "Preferences -> General".<br>2. Go to PROJECTS in the navigation on the left.<br> 3. Now click on the three dots on the right of the project that you want to export and select Export.<br> 4. Now click Export -> Save as CSV. The Export dropdown is in the header of the export table in the top right corner.`},
	{"key": "solidtime", "name": "Solidtime", "description": `1. Choose the organization you want to export in dropdown in the left top corner<br>2. Click on "Export" in the left navigation under "Admin" (You need to be Admin or Owner of the organization to see this)<br>3. Click on "Export". <br>4. Save the file and upload it here.`},
	{"key": "harvest_projects", "name": "Harvest Projects", "description": `1. Go to "Projects" (top navigation)<br>2. Click on the "Export" button <br>3. Select which projects you would like to export and select CSV format <br><br>Before you import make sure that the Timezone settings in Harvest are the same as in solidtime.`},
	{"key": "harvest_time_entries", "name": "Harvest Time Entries", "description": `<strong>Important:</strong> If you also want to import your projects use the "Harvest Projects" importer before this one, since that export contains more details such as billable status and estimated time.<br><br>1. Go to Settings (right top corner)<br>2. Click on "Import/Export" in the left navigation <br>3. Now click on "Export all time" <br><br>Before you import make sure that the Timezone settings in Harvest are the same as in solidtime.`},
	{"key": "harvest_clients", "name": "Harvest Clients", "description": `1. Go to "Manage" (top navigation)<br>2. Click on the "Clients" <br>3. Click on "Import/Export" and in the dropdown "Export clients to CSV" <br>`},
	{"key": "generic_projects", "name": "Generic Projects", "description": `If you want to import many projects yourself this importer the right choice. Please see our docs for <a href="https://docs.solidtime.io/user-guide/import">more information about the CSV structure</a>`},
	{"key": "generic_time_entries", "name": "Generic Time Entries", "description": `If you want to import many time entries yourself this importer the right choice. Please see our docs for <a href="https://docs.solidtime.io/user-guide/import">more information about the CSV structure</a>`},
}

func (s *Server) handleImporters(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "import")
	if !ok {
		return
	}
	_ = scope
	writeJSON(w, http.StatusOK, map[string]any{"data": importerInfo})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "import")
	if !ok {
		return
	}
	var body struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	errs := map[string][]string{}
	if body.Type == "" {
		errs["type"] = []string{"The type field is required."}
	}
	// config('import.max_data_size') = 50 MB on the base64 string.
	if body.Data == "" {
		errs["data"] = []string{"The data field is required."}
	} else if len(body.Data) > 50*1024*1024 {
		errs["data"] = []string{"The data field must not be greater than 52428800 characters."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	data, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		writeMessage(w, http.StatusBadRequest, "Invalid base64 encoded data")
		return
	}
	if !importer.ImporterKeys()[body.Type] {
		writeMessage(w, http.StatusBadRequest, "Invalid importer type")
		return
	}
	tz := "UTC"
	if u := auth.UserFromContext(r.Context()); u != nil && u.Timezone != "" {
		tz = u.Timezone
	}
	report, err := s.importer.Import(r.Context(), scope.org, body.Type, data, tz)
	if err != nil {
		var ie *importer.Error
		if errors.As(err, &ie) {
			writeMessage(w, http.StatusBadRequest, ie.Error())
			return
		}
		writeMessage(w, http.StatusBadRequest, "Import failed, please try again later or contact support")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report.Map()})
}

// ---- currencies / timezones (public like upstream) ----

func (s *Server) handleCurrencies(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]string, 0)
	for _, c := range domain.CurrencyList() {
		out = append(out, map[string]string{"code": c.Code, "name": c.Name, "symbol": c.Symbol})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTimeZones(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]string, 0)
	for _, tz := range domain.TimezoneList() {
		out = append(out, map[string]string{"key": tz})
	}
	writeJSON(w, http.StatusOK, out)
}
