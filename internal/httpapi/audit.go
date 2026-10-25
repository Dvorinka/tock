package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/audit"
	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// auditAliasLabels are the auditable types a client may filter on.
var auditAliasLabels = map[string]string{
	audit.TypeClient:                 "Client",
	audit.TypeInvoice:                "Invoice",
	audit.TypeInvoiceRecipient:       "Invoice recipient",
	audit.TypeMember:                 "Member",
	audit.TypeOrganization:           "Organization",
	audit.TypeOrganizationInvitation: "Invitation",
	audit.TypeProject:                "Project",
	audit.TypeProjectMember:          "Project member",
	audit.TypeTag:                    "Tag",
	audit.TypeTask:                   "Task",
	audit.TypeTimeEntry:              "Time entry",
	audit.TypeUser:                   "User",
}

var auditEvents = map[string]bool{
	audit.EventCreated: true,
	audit.EventUpdated: true,
	audit.EventDeleted: true,
	"restored":         true,
}

// auditMeta captures who made the change and from where. Upstream resolves the
// same values through the auditing package's resolvers.
func (s *Server) auditMeta(r *http.Request) audit.Meta {
	var actorID pgtype.UUID
	if u := auth.UserFromContext(r.Context()); u != nil {
		if id, ok := domain.UUID(u.ID); ok {
			actorID = id
		}
	}
	url := strings.TrimRight(s.appURL, "/") + r.URL.RequestURI()
	return audit.Meta{
		ActorID:   actorID,
		URL:       url,
		IP:        audit.AnonymizeIP(clientIP(r)),
		UserAgent: r.UserAgent(),
	}
}

// clientIP prefers the proxy chain when the request carries one, like
// Laravel's Request::ip() with trusted proxies.
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if ip := strings.TrimSpace(first); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// recordAudit writes the audit row for a mutation. It never fails the request:
// the mutation is already committed, and losing the log line must not turn a
// successful change into an error response (upstream behaves the same way).
func (s *Server) recordAudit(r *http.Request, event, alias string, id, orgID, ownerUserID pgtype.UUID, before, after map[string]any) {
	var oldValues, newValues map[string]any
	switch event {
	case audit.EventCreated:
		newValues = after
	case audit.EventDeleted:
		oldValues = before
	default:
		oldValues, newValues = audit.Diff(before, after)
	}
	err := audit.Record(r.Context(), db.New(s.pool), audit.Entry{
		Meta:        s.auditMeta(r),
		Event:       event,
		Type:        alias,
		AuditableID: id,
		OwnerOrgID:  orgID,
		OwnerUserID: ownerUserID,
		OldValues:   oldValues,
		NewValues:   newValues,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "record audit", "type", alias, "event", event, "error", err)
	}
}

// ---- attribute snapshots ----
//
// Snapshots list the audited columns of each model — everything except the
// timestamps (audit.timestamps is false upstream) and the per-model excludes.

// uuidOrZero parses a uuid string, returning the zero value when it does not
// parse (the column is then written as NULL).
func uuidOrZero(raw string) pgtype.UUID {
	id, _ := domain.UUID(raw)
	return id
}

func auditUID(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return domain.UUIDString(id)
}

func auditText(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func auditDate(value pgtype.Date) any {
	if !value.Valid {
		return nil
	}
	return value.Time.Format(ymdLayout)
}

func auditTime(value pgtype.Timestamp) any {
	if !value.Valid {
		return nil
	}
	return value.Time.Format("2006-01-02 15:04:05")
}

func clientAttrs(c db.Client) map[string]any {
	return map[string]any{
		"id":              auditUID(c.ID),
		"organization_id": auditUID(c.OrganizationID),
		"name":            c.Name,
		"archived_at":     auditTime(c.ArchivedAt),
	}
}

func tagAttrs(t db.Tag) map[string]any {
	return map[string]any{
		"id":              auditUID(t.ID),
		"organization_id": auditUID(t.OrganizationID),
		"name":            t.Name,
	}
}

// projectAttrs excludes `spent_time`, like Project::$auditExclude.
func projectAttrs(p db.Project) map[string]any {
	return map[string]any{
		"id":              auditUID(p.ID),
		"organization_id": auditUID(p.OrganizationID),
		"client_id":       auditUID(p.ClientID),
		"name":            p.Name,
		"color":           p.Color,
		"billable_rate":   p.BillableRate,
		"is_billable":     p.IsBillable,
		"is_public":       p.IsPublic,
		"estimated_time":  p.EstimatedTime,
		"archived_at":     auditTime(p.ArchivedAt),
	}
}

// taskAttrs excludes `spent_time`, like Task::$auditExclude.
func taskAttrs(t db.Task) map[string]any {
	return map[string]any{
		"id":              auditUID(t.ID),
		"organization_id": auditUID(t.OrganizationID),
		"project_id":      auditUID(t.ProjectID),
		"name":            t.Name,
		"done_at":         auditTime(t.DoneAt),
		"estimated_time":  t.EstimatedTime,
	}
}

func projectMemberRowAttrs(m db.GetProjectMemberByIDRow) map[string]any {
	return map[string]any{
		"id":            auditUID(m.ID),
		"project_id":    auditUID(m.ProjectID),
		"member_id":     auditUID(m.MemberID),
		"user_id":       auditUID(m.UserID),
		"billable_rate": m.BillableRate,
	}
}

func projectMemberAttrs(m db.ProjectMember) map[string]any {
	return map[string]any{
		"id":            auditUID(m.ID),
		"project_id":    auditUID(m.ProjectID),
		"member_id":     auditUID(m.MemberID),
		"user_id":       auditUID(m.UserID),
		"billable_rate": m.BillableRate,
	}
}

// timeEntryAttrs excludes `billable_rate`, like TimeEntry::$auditExclude.
func timeEntryAttrs(entry db.TimeEntry) map[string]any {
	var tags any
	if len(entry.Tags) > 0 {
		var parsed []string
		if err := json.Unmarshal(entry.Tags, &parsed); err == nil {
			tags = parsed
		}
	}
	return map[string]any{
		"id":                         auditUID(entry.ID),
		"organization_id":            auditUID(entry.OrganizationID),
		"user_id":                    auditUID(entry.UserID),
		"member_id":                  auditUID(entry.MemberID),
		"project_id":                 auditUID(entry.ProjectID),
		"task_id":                    auditUID(entry.TaskID),
		"client_id":                  auditUID(entry.ClientID),
		"description":                entry.Description,
		"start":                      auditTime(entry.Start),
		"end":                        auditTime(entry.End),
		"billable":                   entry.Billable,
		"tags":                       tags,
		"type":                       entry.Type,
		"is_imported":                entry.IsImported,
		"still_active_email_sent_at": auditTime(entry.StillActiveEmailSentAt),
	}
}

func memberAttrs(m db.Member) map[string]any {
	return map[string]any{
		"id":              auditUID(m.ID),
		"organization_id": auditUID(m.OrganizationID),
		"user_id":         auditUID(m.UserID),
		"role":            auditText(m.Role),
		"billable_rate":   m.BillableRate,
	}
}

func invitationAttrs(i db.OrganizationInvitation) map[string]any {
	return map[string]any{
		"id":              auditUID(i.ID),
		"organization_id": auditUID(i.OrganizationID),
		"email":           i.Email,
		"role":            auditText(i.Role),
		"accepted_at":     auditTime(i.AcceptedAt),
	}
}

func organizationAttrs(o db.Organization) map[string]any {
	return map[string]any{
		"id":                               auditUID(o.ID),
		"user_id":                          auditUID(o.UserID),
		"name":                             o.Name,
		"personal_team":                    o.PersonalTeam,
		"billable_rate":                    o.BillableRate,
		"currency":                         o.Currency,
		"employees_can_see_billable_rates": o.EmployeesCanSeeBillableRates,
		"employees_can_manage_tasks":       o.EmployeesCanManageTasks,
		"prevent_overlapping_time_entries": o.PreventOverlappingTimeEntries,
		"breaks_enabled":                   o.BreaksEnabled,
		"number_format":                    o.NumberFormat,
		"currency_format":                  o.CurrencyFormat,
		"date_format":                      o.DateFormat,
		"interval_format":                  o.IntervalFormat,
		"time_format":                      o.TimeFormat,
	}
}

// authUserAttrs snapshots the profile fields a user can change about
// themselves. Credentials and 2FA material stay out (User::$hidden upstream).
func authUserAttrs(u auth.User) map[string]any {
	return map[string]any{
		"id":                                  u.ID,
		"name":                                u.Name,
		"email":                               u.Email,
		"pending_email":                       auditText(u.PendingEmail),
		"timezone":                            u.Timezone,
		"week_start":                          u.WeekStart,
		"profile_photo_path":                  auditText(u.ProfilePhotoPath),
		"current_team_id":                     auditText(u.CurrentOrganizationID),
		"email_verified_at":                   auditTimePtr(u.EmailVerifiedAt),
		"send_time_entry_still_running_email": u.SendStillRunningEmail,
	}
}

func auditTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format("2006-01-02 15:04:05")
}

func invoiceAttrs(inv invoiceRecord) map[string]any {
	return map[string]any{
		"id":                       auditUID(inv.ID),
		"organization_id":          auditUID(inv.OrganizationID),
		"invoice_recipient_id":     auditUID(inv.InvoiceRecipientID),
		"status":                   inv.Status,
		"date":                     auditDate(inv.Date),
		"due_at":                   auditDate(inv.DueAt),
		"paid_date":                auditDate(inv.PaidDate),
		"reference":                inv.Reference,
		"seller_name":              inv.SellerName,
		"seller_vatin":             auditText(inv.SellerVatin),
		"seller_address_line_1":    auditText(inv.SellerAddressLine1),
		"seller_address_line_2":    auditText(inv.SellerAddressLine2),
		"seller_address_line_3":    auditText(inv.SellerAddressLine3),
		"seller_address_post_code": auditText(inv.SellerAddressPostCode),
		"seller_address_city":      auditText(inv.SellerAddressCity),
		"seller_address_country":   auditText(inv.SellerAddressCountry),
		"seller_phone":             auditText(inv.SellerPhone),
		"seller_email":             auditText(inv.SellerEmail),
		"currency":                 inv.Currency,
		"billing_period_start":     auditDate(inv.BillingPeriodStart),
		"billing_period_end":       auditDate(inv.BillingPeriodEnd),
		"tax_rate":                 inv.TaxRate,
		"discount_amount":          inv.DiscountAmount,
		"discount_type":            inv.DiscountType,
		"payment_iban":             auditText(inv.PaymentIban),
		"footer":                   auditText(inv.Footer),
		"notes":                    auditText(inv.Notes),
		"payment_terms":            auditText(inv.PaymentTerms),
		"is_eu_reverse_charge":     inv.IsEuReverseCharge,
	}
}

func invoiceRecipientAttrs(r recipientRecord) map[string]any {
	return map[string]any{
		"id":                auditUID(r.ID),
		"organization_id":   auditUID(r.OrganizationID),
		"name":              r.Name,
		"vatin":             auditText(r.Vatin),
		"address_line_1":    auditText(r.AddressLine1),
		"address_line_2":    auditText(r.AddressLine2),
		"address_line_3":    auditText(r.AddressLine3),
		"address_post_code": auditText(r.AddressPostCode),
		"address_city":      auditText(r.AddressCity),
		"address_country":   auditText(r.AddressCountry),
		"phone":             auditText(r.Phone),
		"email":             auditText(r.Email),
		"archived_at":       auditTime(r.ArchivedAt),
	}
}

// ---- read endpoint ----

// auditRow is the shared shape of the list and detail queries.
type auditRow struct {
	ID                  int64
	ActorType           *string
	ActorID             pgtype.UUID
	ActorName           *string
	Event               string
	AuditableType       string
	AuditableID         pgtype.UUID
	OldValues           []byte
	NewValues           []byte
	Url                 *string
	IpAddress           string
	UserAgent           *string
	Tags                *string
	OwnerUserID         pgtype.UUID
	OwnerOrganizationID pgtype.UUID
	CreatedAt           pgtype.Timestamp
	UpdatedAt           pgtype.Timestamp
}

func auditRowFromList(row db.ListAuditsPaginatedRow) auditRow { return auditRow(row) }
func auditRowFromGet(row db.GetAuditRow) auditRow             { return auditRow(row) }

func auditResource(row auditRow) map[string]any {
	return map[string]any{
		"id":                    row.ID,
		"actor_type":            auditText(row.ActorType),
		"actor_id":              auditUID(row.ActorID),
		"actor_name":            auditText(row.ActorName),
		"event":                 row.Event,
		"auditable_type":        row.AuditableType,
		"auditable_label":       auditAliasLabels[row.AuditableType],
		"auditable_id":          auditUID(row.AuditableID),
		"old_values":            jsonValue(row.OldValues),
		"new_values":            jsonValue(row.NewValues),
		"url":                   auditText(row.Url),
		"ip_address":            row.IpAddress,
		"user_agent":            auditText(row.UserAgent),
		"tags":                  auditText(row.Tags),
		"owner_user_id":         auditUID(row.OwnerUserID),
		"owner_organization_id": auditUID(row.OwnerOrganizationID),
		"created_at":            tsFormat(row.CreatedAt),
		"updated_at":            tsFormat(row.UpdatedAt),
	}
}

// jsonValue renders a json/jsonb column as JSON (an object, or [] when empty).
func jsonValue(raw []byte) any {
	if len(raw) == 0 {
		return []any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// handleListAudits: GET /api/v1/organizations/{organization}/audits
func (s *Server) handleListAudits(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "audits:view")
	if !ok {
		return
	}
	query := r.URL.Query()
	errs := map[string][]string{}

	params := db.ListAuditsPaginatedParams{
		OrganizationID: scope.org.ID,
		PageLimit:      defaultPerPage,
	}
	countParams := db.CountAuditsParams{OrganizationID: scope.org.ID}

	if raw := query.Get("actor"); raw != "" {
		id, ok := domain.UUID(raw)
		if !ok {
			errs["actor"] = []string{"The selected actor is invalid."}
		} else {
			params.ActorID = id
			countParams.ActorID = id
		}
	}
	if raw := query.Get("event"); raw != "" {
		if !auditEvents[raw] {
			errs["event"] = []string{"The selected event is invalid."}
		} else {
			params.Event = &raw
			countParams.Event = &raw
		}
	}
	if raw := query.Get("model"); raw != "" && raw != "all" {
		if _, known := auditAliasLabels[raw]; !known {
			errs["model"] = []string{"The selected model is invalid."}
		} else {
			params.AuditableType = &raw
			countParams.AuditableType = &raw
		}
	}
	if raw := query.Get("auditable_id"); raw != "" {
		id, ok := domain.UUID(raw)
		if !ok {
			errs["auditable_id"] = []string{"The selected auditable id is invalid."}
		} else {
			params.AuditableID = id
			countParams.AuditableID = id
		}
	}
	if raw := query.Get("from"); raw != "" {
		at, err := time.ParseInLocation(ymdLayout, raw, time.UTC)
		if err != nil {
			errs["from"] = []string{"The from field must be a valid date."}
		} else {
			stamp := pgtype.Timestamp{Time: at, Valid: true}
			params.FromAt = stamp
			countParams.FromAt = stamp
		}
	}
	if raw := query.Get("to"); raw != "" {
		at, err := time.ParseInLocation(ymdLayout, raw, time.UTC)
		if err != nil {
			errs["to"] = []string{"The to field must be a valid date."}
		} else {
			// inclusive: the whole day matches
			stamp := pgtype.Timestamp{Time: at.Add(24*time.Hour - time.Second), Valid: true}
			params.ToAt = stamp
			countParams.ToAt = stamp
		}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}

	page := pageParam(r)
	params.PageOffset = int32((page - 1) * defaultPerPage)

	q := db.New(s.pool)
	total, err := q.CountAudits(r.Context(), countParams)
	if err != nil {
		slog.ErrorContext(r.Context(), "count audits", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rows, err := q.ListAuditsPaginated(r.Context(), params)
	if err != nil {
		slog.ErrorContext(r.Context(), "list audits", "error", err)
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		data = append(data, auditResource(auditRowFromList(row)))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

// handleShowAudit: GET .../audits/{audit} — the detail panel of the viewer.
func (s *Server) handleShowAudit(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "audits:view")
	if !ok {
		return
	}
	raw := chi.URLParam(r, "audit")
	var id int64
	if _, err := fmt.Sscan(raw, &id); err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Audit", raw))
		return
	}
	row, err := db.New(s.pool).GetAudit(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Audit", raw))
		return
	}
	if row.OwnerOrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": auditResource(auditRowFromGet(row))})
}
