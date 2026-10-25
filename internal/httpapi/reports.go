package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// ---- ReportPropertiesDto port ----

// reportProperties mirrors upstream ReportPropertiesDto. The JSONB column uses
// camelCase keys (Laravel cast); the API surfaces them as snake_case.
type reportProperties struct {
	Group           string
	SubGroup        string
	HistoryGroup    string
	WeekStart       string
	Timezone        string
	Start           time.Time
	End             time.Time
	Active          *bool
	MemberIDs       *[]string
	Billable        *bool
	ClientIDs       *[]string
	ProjectIDs      *[]string
	TagIDs          *[]string
	TagMatchType    *string
	TaskIDs         *[]string
	RoundingType    *string
	RoundingMinutes *int64
	TimeEntryType   *string
}

type reportPropertiesJSON struct {
	Group           string    `json:"group"`
	SubGroup        string    `json:"subGroup"`
	HistoryGroup    string    `json:"historyGroup"`
	WeekStart       string    `json:"weekStart"`
	Timezone        string    `json:"timezone"`
	Start           string    `json:"start"`
	End             string    `json:"end"`
	Active          *bool     `json:"active"`
	MemberIDs       *[]string `json:"memberIds"`
	Billable        *bool     `json:"billable"`
	ClientIDs       *[]string `json:"clientIds"`
	ProjectIDs      *[]string `json:"projectIds"`
	TagIDs          *[]string `json:"tagIds"`
	TagMatchType    *string   `json:"tagMatchType"`
	TaskIDs         *[]string `json:"taskIds"`
	RoundingType    *string   `json:"roundingType"`
	RoundingMinutes *int64    `json:"roundingMinutes"`
	TimeEntryType   *string   `json:"timeEntryType"`
}

func (p *reportProperties) MarshalJSON() ([]byte, error) {
	return json.Marshal(reportPropertiesJSON{
		Group: p.Group, SubGroup: p.SubGroup, HistoryGroup: p.HistoryGroup,
		WeekStart: p.WeekStart, Timezone: p.Timezone,
		Start:           p.Start.UTC().Format("2006-01-02T15:04:05Z"),
		End:             p.End.UTC().Format("2006-01-02T15:04:05Z"),
		Active:          p.Active,
		MemberIDs:       p.MemberIDs,
		Billable:        p.Billable,
		ClientIDs:       p.ClientIDs,
		ProjectIDs:      p.ProjectIDs,
		TagIDs:          p.TagIDs,
		TagMatchType:    p.TagMatchType,
		TaskIDs:         p.TaskIDs,
		RoundingType:    p.RoundingType,
		RoundingMinutes: p.RoundingMinutes,
		TimeEntryType:   p.TimeEntryType,
	})
}

func (p *reportProperties) UnmarshalJSON(b []byte) error {
	var j reportPropertiesJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	p.Group, p.SubGroup, p.HistoryGroup = j.Group, j.SubGroup, j.HistoryGroup
	p.WeekStart, p.Timezone = j.WeekStart, j.Timezone
	if t, err := time.Parse("2006-01-02T15:04:05Z", j.Start); err == nil {
		p.Start = t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", j.End); err == nil {
		p.End = t
	}
	p.Active, p.Billable = j.Active, j.Billable
	p.MemberIDs, p.ClientIDs = j.MemberIDs, j.ClientIDs
	p.ProjectIDs, p.TagIDs, p.TaskIDs = j.ProjectIDs, j.TagIDs, j.TaskIDs
	p.TagMatchType = j.TagMatchType
	p.RoundingType, p.RoundingMinutes = j.RoundingType, j.RoundingMinutes
	p.TimeEntryType = j.TimeEntryType
	return nil
}

// aggregationFromInterval ports TimeEntryAggregationType::fromInterval.
func aggregationFromInterval(interval string) string {
	switch interval {
	case "week":
		return "week"
	case "month":
		return "month"
	case "year":
		return "year"
	default:
		return "day"
	}
}

// ---- resources ----

func tsFormat(t pgtype.Timestamp) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC().Format("2006-01-02T15:04:05Z")
}

func (s *Server) shareableLink(rep db.Report) any {
	if rep.IsPublic && rep.ShareSecret != nil {
		return strings.TrimRight(s.appURL, "/") + "/shared-report#" + *rep.ShareSecret
	}
	return nil
}

func (s *Server) reportResource(rep db.Report, detailed bool) map[string]any {
	m := map[string]any{
		"id":             domain.UUIDString(rep.ID),
		"name":           rep.Name,
		"description":    rep.Description,
		"is_public":      rep.IsPublic,
		"public_until":   tsFormat(rep.PublicUntil),
		"shareable_link": s.shareableLink(rep),
		"created_at":     tsFormat(rep.CreatedAt),
		"updated_at":     tsFormat(rep.UpdatedAt),
	}
	if detailed {
		var props reportProperties
		if err := json.Unmarshal(rep.Properties, &props); err == nil {
			m["properties"] = map[string]any{
				"group":            props.Group,
				"sub_group":        props.SubGroup,
				"history_group":    props.HistoryGroup,
				"start":            props.Start.UTC().Format("2006-01-02T15:04:05Z"),
				"end":              props.End.UTC().Format("2006-01-02T15:04:05Z"),
				"active":           props.Active,
				"member_ids":       props.MemberIDs,
				"billable":         props.Billable,
				"time_entry_type":  props.TimeEntryType,
				"client_ids":       props.ClientIDs,
				"project_ids":      props.ProjectIDs,
				"tag_ids":          props.TagIDs,
				"tag_match_type":   props.TagMatchType,
				"task_ids":         props.TaskIDs,
				"rounding_type":    props.RoundingType,
				"rounding_minutes": props.RoundingMinutes,
			}
		}
	}
	return m
}

// ---- handlers ----

func (s *Server) resolveReport(w http.ResponseWriter, r *http.Request, perm string) (*orgScope, *db.Report, bool) {
	scope, ok := s.resolveOrg(w, r, perm)
	if !ok {
		return nil, nil, false
	}
	id, isUUID := domain.UUID(chi.URLParam(r, "report"))
	rep, err := db.New(s.pool).GetReportByID(r.Context(), id)
	if !isUUID || err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Report", chi.URLParam(r, "report")))
		return nil, nil, false
	}
	if rep.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return nil, nil, false
	}
	return scope, &rep, true
}

func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "reports:view")
	if !ok {
		return
	}
	page := pageParam(r)
	q := db.New(s.pool)
	total, err := q.CountReports(r.Context(), scope.org.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rows, err := q.ListReportsPaginated(r.Context(), db.ListReportsPaginatedParams{
		OrganizationID: scope.org.ID,
		Limit:          defaultPerPage,
		Offset:         int32((page - 1) * int(defaultPerPage)),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, rep := range rows {
		items = append(items, s.reportResource(rep, false))
	}
	paginatedResponse(w, r, items, total, page, defaultPerPage)
}

func (s *Server) handleShowReport(w http.ResponseWriter, r *http.Request) {
	_, rep, ok := s.resolveReport(w, r, "reports:view")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": s.reportResource(*rep, true)})
}

func (s *Server) handleStoreReport(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "reports:create")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}
	name, namePresent, nameValid := jsonStr(body, "name")
	switch {
	case !namePresent:
		errs["name"] = []string{"The name field is required."}
	case !nameValid:
		errs["name"] = []string{"The name must be a string."}
	case len(name) > 255:
		errs["name"] = []string{"The name must not be greater than 255 characters."}
	}
	// nullable|string: an explicit "" is stored as "", absent/null as null.
	var description *string
	if raw, present := body["description"]; present && string(raw) != "null" {
		if s2, _, ok := jsonStr(body, "description"); ok {
			description = &s2
		} else {
			errs["description"] = []string{"The description must be a string."}
		}
	}
	isPublic, pubPresent, pubValid := jsonBool(body, "is_public")
	if !pubPresent {
		errs["is_public"] = []string{"The is public field is required."}
	} else if !pubValid {
		errs["is_public"] = []string{"The is public field must be true or false."}
	}
	publicUntil := parseNullableZulu(body, "public_until", errs)
	var propsRaw map[string]json.RawMessage
	if p, present := body["properties"]; !present {
		errs["properties"] = []string{"The properties field is required."}
	} else if json.Unmarshal(p, &propsRaw) != nil {
		errs["properties"] = []string{"The properties must be an array."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	u := auth.UserFromContext(r.Context())
	props, propErrs := parseReportProperties(propsRaw, u)
	if len(propErrs) > 0 {
		validationErr(w, propErrs)
		return
	}
	var secret *string
	if isPublic {
		gen := generateSecret(40)
		secret = &gen
	} else {
		publicUntil = pgtype.Timestamp{}
	}
	propsJSON, err := json.Marshal(&props)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	id, _ := domain.UUID(newUUID())
	_, err = db.New(s.pool).InsertReport(r.Context(), db.InsertReportParams{
		ID:             id,
		Name:           name,
		Description:    description,
		IsPublic:       isPublic,
		ShareSecret:    secret,
		Properties:     propsJSON,
		PublicUntil:    publicUntil,
		OrganizationID: scope.org.ID,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rep, err := db.New(s.pool).GetReportByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": s.reportResource(rep, true)})
}

// parseReportProperties ports ReportStoreRequest's nested properties rules.
// Defaults for week_start/timezone fall back to the creating user.
func parseReportProperties(p map[string]json.RawMessage, u *auth.User) (reportProperties, map[string][]string) {
	errs := map[string][]string{}
	var props reportProperties

	if v, present := p["group"]; !present {
		errs["properties.group"] = []string{"The group field is required."}
	} else if s2, ok := rawString(v); !ok || !aggregationTypes[s2] {
		errs["properties.group"] = []string{"The selected group is invalid."}
	} else {
		props.Group = s2
	}
	if v, present := p["sub_group"]; !present {
		errs["properties.sub_group"] = []string{"The sub group field is required."}
	} else if s2, ok := rawString(v); !ok || !aggregationTypes[s2] {
		errs["properties.sub_group"] = []string{"The selected sub group is invalid."}
	} else {
		props.SubGroup = s2
	}
	if v, present := p["history_group"]; !present {
		errs["properties.history_group"] = []string{"The history group field is required."}
	} else if s2, ok := rawString(v); !ok || (s2 != "day" && s2 != "week" && s2 != "month" && s2 != "year") {
		errs["properties.history_group"] = []string{"The selected history group is invalid."}
	} else {
		props.HistoryGroup = s2
	}
	for _, key := range []string{"start", "end"} {
		v, present := p[key]
		if !present {
			errs["properties."+key] = []string{"The " + key + " field is required."}
			continue
		}
		s2, ok := rawString(v)
		t, parsed := parseZulu(s2)
		if !ok || !parsed {
			errs["properties."+key] = []string{"The " + key + " does not match the format Y-m-d\\TH:i:s\\Z."}
			continue
		}
		if key == "start" {
			props.Start = t
		} else {
			props.End = t
		}
	}
	if v, present := p["active"]; present {
		if b, ok := rawBool(v); ok {
			props.Active = &b
		}
	}
	if v, present := p["billable"]; present {
		if b, ok := rawBool(v); ok {
			props.Billable = &b
		}
	}
	props.MemberIDs = parseStringListOrErr(p, "member_ids", false, errs)
	props.ClientIDs = parseStringListOrErr(p, "client_ids", true, errs)
	props.ProjectIDs = parseStringListOrErr(p, "project_ids", true, errs)
	props.TagIDs = parseStringListOrErr(p, "tag_ids", true, errs)
	props.TaskIDs = parseStringListOrErr(p, "task_ids", true, errs)
	if v, present := p["tag_match_type"]; present {
		if s2, ok := rawString(v); ok && (s2 == "contains" || s2 == "not_contains") {
			props.TagMatchType = &s2
		}
	}
	if v, present := p["week_start"]; present {
		if s2, ok := rawString(v); ok && weekdayValues[s2] {
			props.WeekStart = s2
		}
	}
	if props.WeekStart == "" {
		props.WeekStart = u.WeekStart
	}
	if v, present := p["timezone"]; present {
		if s2, ok := rawString(v); ok && isValidTimezone(s2) {
			props.Timezone = s2
		}
	}
	if props.Timezone == "" {
		props.Timezone = u.Timezone
	}
	if v, present := p["rounding_type"]; present {
		if s2, ok := rawString(v); ok && (s2 == "up" || s2 == "down" || s2 == "nearest") {
			props.RoundingType = &s2
		}
	}
	if v, present := p["rounding_minutes"]; present {
		var n int64
		if json.Unmarshal(v, &n) == nil {
			props.RoundingMinutes = &n
		}
	}
	if v, present := p["time_entry_type"]; present {
		if s2, ok := rawString(v); ok && (s2 == timeEntryTypeWork || s2 == timeEntryTypeBreak) {
			props.TimeEntryType = &s2
		}
	}
	// Upstream stores `properties.time_entry_type` as null when absent, which
	// means the report aggregates work and break entries alike.
	return props, errs
}

var weekdayValues = map[string]bool{
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
	"friday": true, "saturday": true, "sunday": true,
}

func isValidTimezone(tz string) bool {
	for _, z := range domain.TimezoneList() {
		if z == tz {
			return true
		}
	}
	return false
}

func parseStringListOrErr(p map[string]json.RawMessage, key string, allowNone bool, errs map[string][]string) *[]string {
	v, present := p[key]
	if !present || string(v) == "null" {
		return nil
	}
	var list []string
	if json.Unmarshal(v, &list) != nil {
		errs["properties."+key] = []string{"The " + strings.ReplaceAll(key, "_", " ") + " must be an array."}
		return nil
	}
	for i, item := range list {
		if allowNone && item == timeEntryFilterNone {
			continue
		}
		if _, ok := domain.UUID(item); !ok {
			errs[fmt.Sprintf("properties.%s.%d", key, i)] = []string{"The properties." + key + "." + strconv.Itoa(i) + " must be a valid UUID."}
		}
	}
	return &list
}

func (s *Server) handleUpdateReport(w http.ResponseWriter, r *http.Request) {
	_, rep, ok := s.resolveReport(w, r, "reports:update")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	errs := map[string][]string{}
	if _, present := body["name"]; present {
		v, _, valid := jsonStr(body, "name")
		switch {
		case !valid:
			errs["name"] = []string{"The name must be a string."}
		case len(v) > 255:
			errs["name"] = []string{"The name must not be greater than 255 characters."}
		default:
			// Upstream's update rules omit `required`, so "" is accepted.
			rep.Name = v
		}
	}
	if raw, present := body["description"]; present {
		if string(raw) == "null" {
			rep.Description = nil
		} else if s2, _, valid := jsonStr(body, "description"); valid {
			rep.Description = &s2
		} else {
			errs["description"] = []string{"The description must be a string."}
		}
	}
	if _, present := body["is_public"]; present {
		isPublic, _, valid := jsonBool(body, "is_public")
		if !valid {
			errs["is_public"] = []string{"The is public field must be true or false."}
		} else if isPublic != rep.IsPublic {
			rep.IsPublic = isPublic
			if isPublic {
				gen := generateSecret(40)
				rep.ShareSecret = &gen
				rep.PublicUntil = parseNullableZulu(body, "public_until", errs)
			} else {
				rep.ShareSecret = nil
				rep.PublicUntil = pgtype.Timestamp{}
			}
		}
	}
	if rep.IsPublic {
		if _, present := body["public_until"]; present {
			rep.PublicUntil = parseNullableZulu(body, "public_until", errs)
		}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	if err := db.New(s.pool).UpdateReport(r.Context(), db.UpdateReportParams{
		ID: rep.ID, Name: rep.Name, Description: rep.Description,
		IsPublic: rep.IsPublic, ShareSecret: rep.ShareSecret,
		PublicUntil: rep.PublicUntil,
	}); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	updated, err := db.New(s.pool).GetReportByID(r.Context(), rep.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": s.reportResource(updated, true)})
}

func (s *Server) handleDeleteReport(w http.ResponseWriter, r *http.Request) {
	_, rep, ok := s.resolveReport(w, r, "reports:delete")
	if !ok {
		return
	}
	if err := db.New(s.pool).DeleteReport(r.Context(), rep.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicReport ports Public\ReportController::show — X-Api-Key header
// holds the share secret; returns report + aggregated data + history data.
func (s *Server) handlePublicReport(w http.ResponseWriter, r *http.Request) {
	secret := r.Header.Get("X-Api-Key")
	if secret == "" {
		writeMessage(w, http.StatusNotFound, modelNotFound("Report", ""))
		return
	}
	row, err := db.New(s.pool).GetReportByShareSecret(r.Context(), &secret)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Report", ""))
		return
	}
	var props reportProperties
	if err := json.Unmarshal(row.Properties, &props); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	loc, err := time.LoadLocation(props.Timezone)
	if err != nil {
		loc = time.UTC
	}
	roundT, roundM := "", 0
	if props.RoundingType != nil {
		roundT = *props.RoundingType
	}
	if props.RoundingMinutes != nil {
		roundM = int(*props.RoundingMinutes)
	}
	f := s.buildReportFilter(row.OrganizationID, &props)
	data, err := s.aggregate(r.Context(), f, props.Group, props.SubGroup, loc, props.WeekStart,
		false, &props.Start, &props.End, true, roundT, roundM, true)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	histF := s.buildReportFilter(row.OrganizationID, &props)
	historyData, err := s.aggregate(r.Context(), histF, aggregationFromInterval(props.HistoryGroup), "", loc, props.WeekStart,
		true, &props.Start, &props.End, true, roundT, roundM, true)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"name":            row.Name,
			"description":     row.Description,
			"public_until":    tsFormat(row.PublicUntil),
			"currency":        row.Currency,
			"number_format":   row.NumberFormat,
			"currency_format": row.CurrencyFormat,
			"currency_symbol": domain.CurrencySymbol(row.Currency),
			"date_format":     row.DateFormat,
			"interval_format": row.IntervalFormat,
			"time_format":     row.TimeFormat,
			"properties": map[string]any{
				"group":         props.Group,
				"sub_group":     props.SubGroup,
				"history_group": props.HistoryGroup,
				"start":         props.Start.UTC().Format("2006-01-02T15:04:05Z"),
				"end":           props.End.UTC().Format("2006-01-02T15:04:05Z"),
			},
			"data":         data,
			"history_data": historyData,
		},
	})
}

// buildReportFilter converts stored report properties into a teFilter, the
// equivalent of the TimeEntryFilter chain in Public\ReportController::show.
func (s *Server) buildReportFilter(orgID pgtype.UUID, props *reportProperties) *teFilter {
	f := &teFilter{}
	f.add("organization_id = " + f.arg(orgID))
	f.add("start >= " + f.arg(props.Start.UTC()))
	f.add(`start < ` + f.arg(props.End.UTC()))
	if props.Active != nil {
		if *props.Active {
			f.add(`"end" IS NULL`)
		} else {
			f.add(`"end" IS NOT NULL`)
		}
	}
	if props.Billable != nil {
		f.add("billable = " + f.arg(*props.Billable))
	}
	if props.TimeEntryType != nil {
		f.add("type = " + f.arg(*props.TimeEntryType))
	}
	f.addIDList("member_id", parseIDListStr(derefList(props.MemberIDs)))
	f.addIDList("project_id", parseIDListStr(derefList(props.ProjectIDs)))
	match := "contains"
	if props.TagMatchType != nil {
		match = *props.TagMatchType
	}
	f.addTagFilter(parseIDListStr(derefList(props.TagIDs)), match)
	f.addIDList("task_id", parseIDListStr(derefList(props.TaskIDs)))
	f.addIDList("client_id", parseIDListStr(derefList(props.ClientIDs)))
	return f
}

func derefList(l *[]string) []string {
	if l == nil {
		return nil
	}
	return *l
}

// timeEntryFilterNone is TimeEntryFilter::NONE_VALUE — the sentinel a saved
// report uses for "entries without this relation".
const timeEntryFilterNone = "none"

// generateSecret ports ReportService::generateSecret (Str::random(40)).
func generateSecret(n int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b)
}

// ---- shared JSON helpers ----

func rawString(raw json.RawMessage) (string, bool) {
	if string(raw) == "null" {
		return "", true
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	return v, true
}

func rawBool(raw json.RawMessage) (bool, bool) {
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false
	}
	return v, true
}

func parseNullableZulu(body map[string]json.RawMessage, key string, errs map[string][]string) pgtype.Timestamp {
	v, present := body[key]
	if !present || string(v) == "null" {
		return pgtype.Timestamp{}
	}
	label := strings.ReplaceAll(key, "_", " ")
	s, ok := rawString(v)
	t, parsed := parseZulu(s)
	if !ok || !parsed {
		errs[key] = []string{"The " + label + " does not match the format Y-m-d\\TH:i:s\\Z."}
		return pgtype.Timestamp{}
	}
	if !t.After(time.Now()) {
		errs[key] = []string{"The " + label + " must be a date after now."}
		return pgtype.Timestamp{}
	}
	return pgtype.Timestamp{Time: t, Valid: true}
}
