package httpapi

// Time entries — index/filter/aggregation/store/update/multiple/destroy
// ported from TimeEntryController + TimeEntryFilter + TimeEntryService.
// The filter is compositional (Laravel Builder conditionals), so the SQL
// is assembled here with bound parameters rather than sqlc.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

const (
	timeEntryTypeWork  = "work"
	timeEntryTypeBreak = "break"
)

const zuluLayout = "2006-01-02T15:04:05Z"

func parseZulu(s string) (time.Time, bool) {
	t, err := time.Parse(zuluLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func ptts(t time.Time) pgtype.Timestamp { return pgtype.Timestamp{Time: t.UTC(), Valid: true} }

// timeEntryResource ports TimeEntryResource — start/end as ISO8601-Zulu,
// duration null while running, tags as a string ID list.
func timeEntryResource(e db.TimeEntry) map[string]any {
	var end, duration any
	if e.End.Valid {
		end = e.End.Time.UTC().Format(zuluLayout)
		duration = int64(e.End.Time.Sub(e.Start.Time).Seconds())
	}
	var tags []string
	if len(e.Tags) > 0 {
		_ = json.Unmarshal(e.Tags, &tags)
	}
	if tags == nil {
		tags = []string{}
	}
	return map[string]any{
		"id":              domain.UUIDString(e.ID),
		"start":           e.Start.Time.UTC().Format(zuluLayout),
		"end":             end,
		"duration":        duration,
		"description":     e.Description,
		"task_id":         nullableUUID(e.TaskID),
		"project_id":      nullableUUID(e.ProjectID),
		"organization_id": domain.UUIDString(e.OrganizationID),
		"user_id":         domain.UUIDString(e.UserID),
		"tags":            tags,
		"billable":        e.Billable,
		"type":            e.Type,
	}
}

// ---- filter builder (TimeEntryFilter port) ----

type teFilter struct {
	where []string
	args  []any
	n     int
}

func (f *teFilter) arg(v any) string {
	f.n++
	f.args = append(f.args, v)
	return fmt.Sprintf("$%d", f.n)
}

func (f *teFilter) add(clause string) { f.where = append(f.where, clause) }

// uuidList normalizes an ID filter like upstream idArrayToCollection: values
// must be UUIDs or the literal 'none' (included as the null row).
type idList struct {
	ids    []pgtype.UUID
	none   bool
	empty  bool // input present but reduced to nothing and no 'none'
	given  bool
	invalid bool
}

// queryArray returns q[name+"[]"] values (PHP array syntax the SPA uses),
// falling back to a comma-split q[name] single value.
func queryArray(q url.Values, name string) []string {
	if vs, ok := q[name+"[]"]; ok {
		return vs
	}
	if v := q.Get(name); v != "" {
		return strings.Split(v, ",")
	}
	return nil
}

func parseIDList(raw string) idList {
	if raw == "" {
		return idList{}
	}
	return parseIDListStr(strings.Split(raw, ","))
}

func parseIDListStr(raws []string) idList {
	l := idList{}
	if len(raws) == 0 {
		return l
	}
	l.given = true
	l.empty = true
	for _, part := range raws {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == "none" {
			l.none = true
			l.empty = false
			continue
		}
		id, ok := domain.UUID(part)
		if !ok {
			l.invalid = true
			return l
		}
		l.ids = append(l.ids, id)
		l.empty = false
	}
	return l
}

// addIDListFilter ports addProjectIdsFilter/addTaskIdsFilter/addClientIdsFilter:
// `IN (...)` OR `IS NULL` when 'none' is included; empty selection = no filter.
func (f *teFilter) addIDList(column string, l idList) {
	if !l.given || l.invalid {
		return
	}
	if len(l.ids) == 0 && !l.none {
		return
	}
	parts := []string{}
	if len(l.ids) > 0 {
		parts = append(parts, column+" = ANY("+f.arg(uuidBytes(l.ids))+")")
	}
	if l.none {
		parts = append(parts, column+" IS NULL")
	}
	f.add("(" + strings.Join(parts, " OR ") + ")")
}

func uuidBytes(ids []pgtype.UUID) [][]byte {
	out := make([][]byte, len(ids))
	for i, id := range ids {
		b := id.Bytes
		out[i] = b[:]
	}
	return out
}

// addTagFilter ports addTagIdsFilter incl. tag_match_type contains/not_contains.
func (f *teFilter) addTagFilter(l idList, matchType string) {
	if !l.given || l.invalid {
		return
	}
	if len(l.ids) == 0 && !l.none {
		return
	}
	parts := []string{}
	for _, id := range l.ids {
		parts = append(parts, "tags @> to_jsonb("+f.arg(domain.UUIDString(id))+")")
	}
	if l.none {
		parts = append(parts, "(coalesce(jsonb_array_length(tags), 0) = 0)")
	}
	cond := "(" + strings.Join(parts, " OR ") + ")"
	if matchType == "not_contains" {
		if l.none {
			f.add("NOT " + cond)
		} else {
			f.add("(NOT " + cond + " OR tags IS NULL)")
		}
		return
	}
	f.add(cond)
}

// buildTimeEntryWhere assembles the WHERE clause + args for index/aggregate.
// Mirrors getTimeEntriesQuery: org scope + TimeEntryFilter chain, ordered
// start DESC, id.
func (s *Server) buildTimeEntryWhere(r *http.Request, orgID pgtype.UUID, memberFilter *db.Member) *teFilter {
	f := &teFilter{}
	f.add("organization_id = " + f.arg(orgID))
	q := r.URL.Query()

	if v := q.Get("start"); v != "" {
		if t, ok := parseZulu(v); ok {
			f.add("start >= " + f.arg(t))
		}
	}
	if v := q.Get("end"); v != "" {
		if t, ok := parseZulu(v); ok {
			f.add(`start < ` + f.arg(t))
		}
	}
	switch q.Get("active") {
	case "true":
		f.add(`"end" IS NULL`)
	case "false":
		f.add(`"end" IS NOT NULL`)
	}
	if memberFilter != nil {
		f.add("member_id = " + f.arg(memberFilter.ID))
	}
	memberIDs := parseIDListStr(queryArray(q, "member_ids"))
	if memberIDs.given && !memberIDs.invalid && len(memberIDs.ids) > 0 {
		f.add("member_id = ANY(" + f.arg(uuidBytes(memberIDs.ids)) + ")")
	}
	f.addIDList("project_id", parseIDListStr(queryArray(q, "project_ids")))
	f.addTagFilter(parseIDListStr(queryArray(q, "tag_ids")), q.Get("tag_match_type"))
	f.addIDList("task_id", parseIDListStr(queryArray(q, "task_ids")))
	f.addIDList("client_id", parseIDListStr(queryArray(q, "client_ids")))
	switch q.Get("billable") {
	case "true":
		f.add("billable = true")
	case "false":
		f.add("billable = false")
	}
	if t := q.Get("type"); t == timeEntryTypeWork || t == timeEntryTypeBreak {
		f.add("type = " + f.arg(t))
	}
	return f
}

func (f *teFilter) whereClause() string {
	return strings.Join(f.where, " AND ")
}

const timeEntrySelectCols = `id, description, start, "end", billable_rate, billable,
	user_id, organization_id, project_id, task_id, tags, created_at, updated_at,
	member_id, client_id, is_imported, still_active_email_sent_at, type`

func (s *Server) queryTimeEntries(ctx context.Context, f *teFilter, selectCols, orderBy string, limit, offset int) ([]db.TimeEntry, error) {
	sql := "SELECT " + selectCols + " FROM time_entries WHERE " + f.whereClause() + " " + orderBy
	if limit > 0 {
		f.n++
		sql += fmt.Sprintf(" LIMIT $%d", f.n)
		f.args = append(f.args, int32(limit))
	}
	if offset > 0 {
		f.n++
		sql += fmt.Sprintf(" OFFSET $%d", f.n)
		f.args = append(f.args, int32(offset))
	}
	rows, err := s.pool.Query(ctx, sql, f.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[db.TimeEntry])
}

func (s *Server) countTimeEntries(ctx context.Context, f *teFilter) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM time_entries WHERE "+f.whereClause(), f.args...).Scan(&n)
	return n, err
}

// scopedTimeEntry loads {timeEntry}; belongs-to-org is enforced by the
// controller-level check (403), not route binding.
func (s *Server) scopedTimeEntry(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.TimeEntry, bool) {
	var e db.TimeEntry
	id, ok := domain.UUID(chi.URLParam(r, "timeEntry"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("TimeEntry", chi.URLParam(r, "timeEntry")))
		return e, false
	}
	e, err := db.New(s.pool).GetTimeEntryByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("TimeEntry", chi.URLParam(r, "timeEntry")))
		return e, false
	}
	if e.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return e, false
	}
	return e, true
}

// ---- index ----

func (s *Server) handleListTimeEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	var memberFilter *db.Member
	if mid := r.URL.Query().Get("member_id"); mid != "" {
		id, isUUID := domain.UUID(mid)
		m, err := db.New(s.pool).GetMemberByID(r.Context(), id)
		if !isUUID || err != nil {
			writeMessage(w, http.StatusNotFound, modelNotFound("Member", mid))
			return
		}
		if m.ID == scope.member.ID {
			if !scope.has("time-entries:view:own") {
				writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
				return
			}
		} else if !scope.has("time-entries:view:all") {
			writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
			return
		}
		memberFilter = &m
	} else if !scope.has("time-entries:view:all") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 500 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	f := s.buildTimeEntryWhere(r, scope.org.ID, memberFilter)
	total, err := s.countTimeEntries(r.Context(), f)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	entries, err := s.queryTimeEntries(r.Context(), f, timeEntrySelectCols,
		`ORDER BY start DESC, id`, limit, offset)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}

	// only_full_dates: when the page is exactly full, drop entries on the
	// last (possibly partial) day in the user's timezone.
	if len(entries) == limit && r.URL.Query().Get("only_full_dates") == "true" {
		u := auth.UserFromContext(r.Context())
		tz := userTZ(u)
		lastDate := ""
		for _, e := range entries {
			d := e.Start.Time.In(tz).Truncate(24 * time.Hour).Format("2006-01-02")
			if lastDate == "" || d != lastDate {
				lastDate = d
			}
		}
		kept := entries[:0]
		for _, e := range entries {
			if e.Start.Time.In(tz).Format("2006-01-02") != lastDate {
				kept = append(kept, e)
			}
		}
		entries = kept
		// Upstream edge case: a single date can exceed the limit — refetch
		// that whole day capped at 5000.
		if len(entries) == 0 && lastDate != "" {
			dayStart, _ := time.ParseInLocation("2006-01-02", lastDate, tz)
			f2 := s.buildTimeEntryWhere(r, scope.org.ID, memberFilter)
			f2.add("start >= " + f2.arg(dayStart.UTC()))
			f2.add("start <= " + f2.arg(dayStart.Add(24*time.Hour).Add(-time.Second).UTC()))
			entries, _ = s.queryTimeEntries(r.Context(), f2, timeEntrySelectCols,
				`ORDER BY start DESC, id`, 5000, 0)
		}
	}

	data := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		data = append(data, timeEntryResource(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": data,
		"meta": map[string]any{"total": total},
	})
}

func userTZ(u *auth.User) *time.Location {
	if u != nil && u.Timezone != "" {
		if loc, err := time.LoadLocation(u.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// ---- store ----

var runningLocks sync.Map // map[userID]*sync.Mutex — Cache::lock port

func (s *Server) handleStoreTimeEntry(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	errs, vals := s.validateTimeEntryBody(w, r, scope, body, nil, true)
	if errs != nil {
		validationErr(w, errs)
		return
	}
	member := vals.member
	if member.ID == scope.member.ID {
		if !scope.has("time-entries:create:own") {
			writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
			return
		}
	} else if !scope.has("time-entries:create:all") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}

	q := db.New(s.pool)
	// One running entry per user across all orgs — locked like Cache::lock.
	var unlock func()
	if !vals.end.Valid {
		mu, _ := runningLocks.LoadOrStore(domain.UUIDString(member.UserID), &sync.Mutex{})
		m := mu.(*sync.Mutex)
		m.Lock()
		unlock = m.Unlock
		defer unlock()
		running, err := q.CountRunningTimeEntriesForUser(r.Context(), member.UserID)
		if err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		if running > 0 {
			writeDomainErr(w, domain.ErrTimeEntryStillRunning)
			return
		}
	}
	if err := domain.AssertNoOverlap(r.Context(), q, scope.org, member.ID, vals.start, vals.end, pgtype.UUID{}); err != nil {
		writeDomainErr(w, err)
		return
	}

	var clientID pgtype.UUID
	if vals.project.Valid {
		p, err := q.GetProjectByID(r.Context(), vals.project)
		if err == nil {
			clientID = p.ClientID
		}
	}
	rate, err := domain.BillableRateForTimeEntry(r.Context(), q, scope.org.ID, member.UserID, vals.project, vals.billable)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	var tagsJSON []byte
	if len(vals.tags) > 0 {
		tagsJSON, _ = json.Marshal(vals.tags)
	} else {
		tagsJSON, _ = json.Marshal([]string{})
	}
	entry, err := q.InsertTimeEntry(r.Context(), db.InsertTimeEntryParams{
		ID:             pgUUID(),
		Description:    vals.description,
		Start:          vals.start,
		End:            vals.end,
		BillableRate:   rate,
		Billable:       vals.billable,
		UserID:         member.UserID,
		OrganizationID: scope.org.ID,
		ProjectID:      vals.project,
		TaskID:         vals.task,
		Tags:           tagsJSON,
		MemberID:       member.ID,
		ClientID:       clientID,
		Type:           vals.entryType,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if err := domain.RecalculateSpentTime(r.Context(), q, entry.ProjectID, entry.TaskID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": timeEntryResource(entry)})
}

// teVals is the validated payload shared by store/update.
type teVals struct {
	member      db.Member
	project     pgtype.UUID
	task        pgtype.UUID
	start       pgtype.Timestamp
	end         pgtype.Timestamp
	billable    bool
	entryType   string
	description string
	tags        []string
	hasMember   bool
	hasStart    bool
	hasEnd      bool
	endNull     bool
}

// validateTimeEntryBody ports TimeEntryStoreRequest/UpdateRequest rules.
// When `required` is false (update), absent fields are simply skipped.
func (s *Server) validateTimeEntryBody(w http.ResponseWriter, r *http.Request, scope *orgScope, body map[string]json.RawMessage, existing *db.TimeEntry, required bool) (map[string][]string, *teVals) {
	errs := map[string][]string{}
	v := &teVals{entryType: timeEntryTypeWork}
	q := db.New(s.pool)

	resultingType := ""
	if raw, has := body["type"]; has {
		var t string
		if err := json.Unmarshal(raw, &t); err != nil || (t != timeEntryTypeWork && t != timeEntryTypeBreak) {
			errs["type"] = []string{"The selected type is invalid."}
		} else {
			resultingType = t
			v.entryType = t
		}
	} else if existing != nil {
		resultingType = existing.Type
		v.entryType = existing.Type
	} else {
		if required {
			errs["type"] = []string{"The type field is required."}
		}
		resultingType = timeEntryTypeWork
	}
	isBreak := resultingType == timeEntryTypeBreak
	if isBreak && !scope.org.BreaksEnabled {
		errs["type"] = []string{"Breaks are disabled for this organization."}
	}

	// member_id: required on store, exists-in-org
	if raw, has := body["member_id"]; has {
		var mid string
		if err := json.Unmarshal(raw, &mid); err != nil {
			errs["member_id"] = []string{"The member id must be a string."}
		} else if id, ok := domain.UUID(mid); !ok {
			errs["member_id"] = []string{"The member id must be a valid UUID."}
		} else {
			mem, err := scanMember(s.pool.QueryRow(r.Context(),
				`SELECT `+memberCols+` FROM members WHERE id = $1 AND organization_id = $2`,
				id, scope.org.ID))
			if err != nil {
				errs["member_id"] = []string{"The selected member id is invalid."}
			} else {
				v.member = mem
				v.hasMember = true
			}
		}
	} else if required {
		errs["member_id"] = []string{"The member id field is required."}
	}

	// project_id: nullable, required_with task_id, prohibited on break,
	// exists-in-org (+ employee visibility without the :all perms)
	projectPresent := false
	projectNull := false
	if raw, has := body["project_id"]; has {
		projectPresent = true
		if string(raw) == "null" {
			projectNull = true
		} else {
			var pid string
			if err := json.Unmarshal(raw, &pid); err != nil {
				errs["project_id"] = []string{"The project id must be a string."}
			} else if id, ok := domain.UUID(pid); !ok {
				errs["project_id"] = []string{"The project id must be a valid UUID."}
			} else {
				v.project = id
			}
		}
	}
	_, taskPresent := body["task_id"]
	if taskPresent && !projectPresent && required {
		errs["project_id"] = append(errs["project_id"], "The project id field is required when task id is present.")
	}
	if isBreak && projectPresent && !projectNull {
		errs["project_id"] = append(errs["project_id"], "The project id field is prohibited.")
	}
	if v.project.Valid && errs["project_id"] == nil {
		p, err := q.GetProjectByIDInOrg(r.Context(), db.GetProjectByIDInOrgParams{
			ID: v.project, OrganizationID: scope.org.ID,
		})
		if err != nil {
			errs["project_id"] = []string{"The selected project id is invalid."}
		} else {
			permSuffix := "create"
			if !required {
				permSuffix = "update"
			}
			if !scope.has("time-entries:"+permSuffix+":all") && !scope.has("projects:view:all") {
				u := auth.UserFromContext(r.Context())
				uid, _ := domain.UUID(u.ID)
				visible, verr := q.ProjectVisibleToUser(r.Context(), db.ProjectVisibleToUserParams{
					ID: p.ID, OrganizationID: scope.org.ID, UserID: uid,
				})
				if verr != nil || !visible {
					errs["project_id"] = []string{"The selected project id is invalid."}
				}
			}
		}
	}

	// task_id: nullable, prohibited on break, exists-in-org, belongs to project
	if raw, has := body["task_id"]; has {
		if string(raw) != "null" {
			var tid string
			if err := json.Unmarshal(raw, &tid); err != nil {
				errs["task_id"] = []string{"The task id must be a string."}
			} else if id, ok := domain.UUID(tid); !ok {
				errs["task_id"] = []string{"The task id must be a valid UUID."}
			} else {
				v.task = id
			}
		}
		if isBreak && string(raw) != "null" {
			errs["task_id"] = append(errs["task_id"], "The task id field is prohibited.")
		}
	}
	if v.task.Valid {
		effectiveProject := v.project
		if !projectPresent && existing != nil {
			effectiveProject = existing.ProjectID
		}
		okInOrg, err := q.TaskExistsInProject(r.Context(), db.TaskExistsInProjectParams{
			ID: v.task, ProjectID: effectiveProject, OrganizationID: scope.org.ID,
		})
		if err != nil || !okInOrg {
			errs["task_id"] = []string{"The task id is not part of the given project."}
		}
	}

	// start/end: Y-m-d\TH:i:s\Z, end after_or_equal start
	if raw, has := body["start"]; has {
		var sv string
		if err := json.Unmarshal(raw, &sv); err != nil {
			errs["start"] = []string{"The start must be a valid date."}
		} else if t, ok := parseZulu(sv); !ok {
			errs["start"] = []string{"The start does not match the format Y-m-d\\TH:i:s\\Z."}
		} else {
			v.start = ptts(t)
			v.hasStart = true
		}
	} else if required {
		errs["start"] = []string{"The start field is required."}
	}
	if raw, has := body["end"]; has {
		v.hasEnd = true
		if string(raw) == "null" {
			v.endNull = true
		} else {
			var ev string
			if err := json.Unmarshal(raw, &ev); err != nil {
				errs["end"] = []string{"The end must be a valid date."}
			} else if t, ok := parseZulu(ev); !ok {
				errs["end"] = []string{"The end does not match the format Y-m-d\\TH:i:s\\Z."}
			} else {
				v.end = ptts(t)
			}
		}
	}
	effectiveStart := v.start
	if !v.hasStart && existing != nil {
		effectiveStart = existing.Start
	}
	if v.end.Valid && effectiveStart.Valid && v.end.Time.Before(effectiveStart.Time) {
		errs["end"] = append(errs["end"], "The end must be a date after or equal to start.")
	}

	// billable: required bool on store, declined on break
	if raw, has := body["billable"]; has {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			errs["billable"] = []string{"The billable field must be true or false."}
		} else {
			v.billable = b
			if isBreak && b {
				errs["billable"] = append(errs["billable"], "The billable field must be declined.")
			}
		}
	} else if required {
		errs["billable"] = []string{"The billable field is required."}
	} else if existing != nil {
		v.billable = existing.Billable
	}

	// description: nullable max:5000
	if raw, has := body["description"]; has {
		var dsc string
		if err := json.Unmarshal(raw, &dsc); err != nil {
			errs["description"] = []string{"The description must be a string."}
		} else if len(dsc) > 5000 {
			errs["description"] = []string{"The description must not be greater than 5000 characters."}
		} else {
			v.description = dsc
		}
	} else if existing != nil {
		v.description = existing.Description
	}

	// tags: nullable array of in-org tag UUIDs, prohibited on break
	if raw, has := body["tags"]; has {
		if string(raw) == "null" {
			// nullable — leave empty
		} else {
			var tags []string
			if err := json.Unmarshal(raw, &tags); err != nil {
				errs["tags"] = []string{"The tags must be an array."}
			} else {
				for _, t := range tags {
					id, ok := domain.UUID(t)
					if !ok {
						errs["tags"] = append(errs["tags"], "The tags must be a valid UUID.")
						break
					}
					var exists bool
					if err := s.pool.QueryRow(r.Context(),
						`SELECT EXISTS(SELECT 1 FROM tags WHERE id = $1 AND organization_id = $2)`,
						id, scope.org.ID).Scan(&exists); err != nil || !exists {
						errs["tags"] = append(errs["tags"], "The selected tags is invalid.")
						break
					}
					v.tags = append(v.tags, t)
				}
				if isBreak && len(tags) > 0 {
					errs["tags"] = append(errs["tags"], "The tags field is prohibited.")
				}
			}
		}
	} else if existing != nil {
		_ = json.Unmarshal(existing.Tags, &v.tags)
	}

	if len(errs) > 0 {
		return errs, nil
	}
	return nil, v
}

const memberCols = `id, user_id, organization_id, role, billable_rate, created_at, updated_at`

func scanMember(row pgx.Row) (db.Member, error) {
	var m db.Member
	err := row.Scan(&m.ID, &m.UserID, &m.OrganizationID, &m.Role, &m.BillableRate, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// ---- update ----

func (s *Server) handleUpdateTimeEntry(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	entry, ok := s.scopedTimeEntry(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	errs, vals := s.validateTimeEntryBody(w, r, scope, body, &entry, false)
	if errs != nil {
		validationErr(w, errs)
		return
	}

	var newMember *db.Member
	if vals.hasMember {
		newMember = &vals.member
	}
	ownTransition := entry.MemberID == scope.member.ID && (newMember == nil || newMember.ID == scope.member.ID)
	if ownTransition {
		if !scope.has("time-entries:update:own") {
			writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
			return
		}
	} else if !scope.has("time-entries:update:all") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}

	// Restart check: a stopped entry can not be made running again.
	if entry.End.Valid && vals.hasEnd && vals.endNull {
		writeDomainErr(w, domain.ErrTimeEntryCanNotBeRestarted)
		return
	}

	effectiveMember := entry.MemberID
	if vals.hasMember {
		effectiveMember = vals.member.ID
	}
	effectiveStart := entry.Start
	if vals.hasStart {
		effectiveStart = vals.start
	}
	effectiveEnd := entry.End
	if vals.hasEnd {
		effectiveEnd = vals.end
	}
	q := db.New(s.pool)
	if err := domain.AssertNoOverlap(r.Context(), q, scope.org, effectiveMember, effectiveStart, effectiveEnd, entry.ID); err != nil {
		writeDomainErr(w, err)
		return
	}
	if !effectiveEnd.Valid && newMember != nil && newMember.ID != entry.MemberID {
		writeDomainErr(w, domain.ErrTimeEntryMemberWhileRunning)
		return
	}

	oldProject, oldTask := entry.ProjectID, entry.TaskID

	// Apply fields — mirror $timeEntry->fill($request->validated()).
	if vals.hasStart {
		entry.Start = vals.start
	}
	if vals.hasEnd {
		entry.End = vals.end
	}
	if vals.hasMember {
		entry.MemberID = vals.member.ID
		entry.UserID = vals.member.UserID
	}
	projectGiven := false
	if raw, has := body["project_id"]; has {
		projectGiven = true
		if string(raw) == "null" {
			entry.ProjectID = pgtype.UUID{}
			entry.ClientID = pgtype.UUID{}
		} else {
			entry.ProjectID = vals.project
			if p, err := q.GetProjectByID(r.Context(), vals.project); err == nil {
				entry.ClientID = p.ClientID
			}
		}
	}
	taskGiven := false
	if raw, has := body["task_id"]; has {
		taskGiven = true
		if string(raw) == "null" {
			entry.TaskID = pgtype.UUID{}
		} else {
			entry.TaskID = vals.task
		}
	}
	if _, has := body["billable"]; has {
		entry.Billable = vals.billable // canUseBillableRates is always true (no gates)
	}
	if _, has := body["type"]; has {
		entry.Type = vals.entryType
	}
	if _, has := body["description"]; has {
		entry.Description = vals.description
	}
	if _, has := body["tags"]; has {
		tagsJSON, _ := json.Marshal(vals.tags)
		if tagsJSON == nil {
			tagsJSON, _ = json.Marshal([]string{})
		}
		entry.Tags = tagsJSON
	}
	// Project changed but task not given → drop task from a different project.
	if projectGiven && !taskGiven && oldTask.Valid {
		var oldTaskProject pgtype.UUID
		if t, err := q.GetTaskByID(r.Context(), oldTask); err == nil {
			oldTaskProject = t.ProjectID
		}
		if oldTaskProject != entry.ProjectID {
			entry.TaskID = pgtype.UUID{}
		}
	}
	// Break boot behavior: breaks carry no project/task/tags/billable.
	if entry.Type == timeEntryTypeBreak {
		entry.ProjectID = pgtype.UUID{}
		entry.TaskID = pgtype.UUID{}
		entry.ClientID = pgtype.UUID{}
		entry.Tags, _ = json.Marshal([]string{})
		entry.Billable = false
	}

	rate, err := domain.BillableRateForTimeEntry(r.Context(), q, scope.org.ID, entry.UserID, entry.ProjectID, entry.Billable)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	entry.BillableRate = rate

	entry, err = q.UpdateTimeEntry(r.Context(), db.UpdateTimeEntryParams{
		ID: entry.ID, Description: entry.Description, Start: entry.Start, End: entry.End,
		BillableRate: entry.BillableRate, Billable: entry.Billable, UserID: entry.UserID,
		ProjectID: entry.ProjectID, TaskID: entry.TaskID, Tags: entry.Tags,
		MemberID: entry.MemberID, ClientID: entry.ClientID, Type: entry.Type,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	_ = domain.RecalculateSpentTime(r.Context(), q, oldProject, oldTask)
	newProject, newTask := entry.ProjectID, entry.TaskID
	if oldProject != newProject {
		_ = domain.RecalculateSpentTime(r.Context(), q, newProject, pgtype.UUID{})
	}
	if oldTask != newTask {
		_ = domain.RecalculateSpentTime(r.Context(), q, pgtype.UUID{}, newTask)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": timeEntryResource(entry)})
}

// ---- updateMultiple ----

func (s *Server) handleUpdateMultipleTimeEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	if !scope.has("time-entries:update:all") && !scope.has("time-entries:update:own") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	canAccessAll := scope.has("time-entries:update:all")

	var payload struct {
		IDs     []string                   `json:"ids"`
		Changes map[string]json.RawMessage `json:"changes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.IDs) == 0 {
		validationErr(w, map[string][]string{"ids": {"The ids field is required."}})
		return
	}
	changes := payload.Changes
	if changes == nil {
		changes = map[string]json.RawMessage{}
	}

	// Validate change fields once against the shared ruleset.
	changeScope := map[string][]string{}
	var changeMember *db.Member
	if raw, has := changes["member_id"]; has {
		var mid string
		id, isUUID := domain.UUID(mid)
		if err := json.Unmarshal(raw, &mid); err == nil {
			id, isUUID = domain.UUID(mid)
		}
		mem, err := scanMember(s.pool.QueryRow(r.Context(),
			`SELECT `+memberCols+` FROM members WHERE id = $1 AND organization_id = $2`,
			id, scope.org.ID))
		if !isUUID || err != nil {
			changeScope["changes.member_id"] = []string{"The selected member id is invalid."}
		} else {
			changeMember = &mem
		}
		if !canAccessAll && changeMember != nil && changeMember.ID != scope.member.ID {
			writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
			return
		}
	}
	q := db.New(s.pool)
	var changeProject, changeClient pgtype.UUID
	projectChanged := false
	if raw, has := changes["project_id"]; has {
		projectChanged = true
		if string(raw) != "null" {
			var pid string
			id, isUUID := domain.UUID(pid)
			if err := json.Unmarshal(raw, &pid); err == nil {
				id, isUUID = domain.UUID(pid)
			}
			p, err := q.GetProjectByIDInOrg(r.Context(), db.GetProjectByIDInOrgParams{
				ID: id, OrganizationID: scope.org.ID,
			})
			if !isUUID || err != nil {
				changeScope["changes.project_id"] = []string{"The selected project id is invalid."}
			} else {
				changeProject = id
				changeClient = p.ClientID
			}
		}
	}
	var changeTask pgtype.UUID
	taskChanged := false
	if raw, has := changes["task_id"]; has {
		taskChanged = true
		if string(raw) != "null" {
			var tid string
			id, isUUID := domain.UUID(tid)
			if err := json.Unmarshal(raw, &tid); err == nil {
				id, isUUID = domain.UUID(tid)
			}
			okInOrg, err := q.TaskExistsInProject(r.Context(), db.TaskExistsInProjectParams{
				ID: id, ProjectID: changeProject, OrganizationID: scope.org.ID,
			})
			if !isUUID || err != nil || !okInOrg {
				changeScope["changes.task_id"] = []string{"The task id is not part of the given project."}
			} else {
				changeTask = id
			}
		}
	}
	var changeType string
	if raw, has := changes["type"]; has {
		var t string
		if err := json.Unmarshal(raw, &t); err != nil || (t != timeEntryTypeWork && t != timeEntryTypeBreak) {
			changeScope["changes.type"] = []string{"The selected type is invalid."}
		} else {
			changeType = t
		}
	}
	var changeBillable *bool
	if raw, has := changes["billable"]; has {
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			changeScope["changes.billable"] = []string{"The billable field must be true or false."}
		} else {
			changeBillable = &b
		}
	}
	var changeDesc *string
	if raw, has := changes["description"]; has {
		var dsc string
		if string(raw) == "null" {
			d := ""
			changeDesc = &d
		} else if err := json.Unmarshal(raw, &dsc); err != nil {
			changeScope["changes.description"] = []string{"The description must be a string."}
		} else {
			changeDesc = &dsc
		}
	}
	var changeStart, changeEnd *time.Time
	if raw, has := changes["start"]; has {
		var sv string
		if err := json.Unmarshal(raw, &sv); err != nil {
			changeScope["changes.start"] = []string{"The start must be a valid date."}
		} else if t, ok := parseZulu(sv); !ok {
			changeScope["changes.start"] = []string{"The start does not match the format Y-m-d\\TH:i:s\\Z."}
		} else {
			changeStart = &t
		}
	}
	if raw, has := changes["end"]; has {
		if string(raw) == "null" {
			changeEnd = &time.Time{} // marker: explicitly null → restart attempts rejected per-entry below
		} else {
			var ev string
			if err := json.Unmarshal(raw, &ev); err != nil {
				changeScope["changes.end"] = []string{"The end must be a valid date."}
			} else if t, ok := parseZulu(ev); !ok {
				changeScope["changes.end"] = []string{"The end does not match the format Y-m-d\\TH:i:s\\Z."}
			} else {
				changeEnd = &t
			}
		}
	}
	var changeTags *[]string
	if raw, has := changes["tags"]; has {
		if string(raw) == "null" {
			empty := []string{}
			changeTags = &empty
		} else {
			var tags []string
			if err := json.Unmarshal(raw, &tags); err != nil {
				changeScope["changes.tags"] = []string{"The tags must be an array."}
			} else {
				for _, t := range tags {
					id, ok := domain.UUID(t)
					if !ok {
						changeScope["changes.tags"] = append(changeScope["changes.tags"], "The tags must be a valid UUID.")
						break
					}
					var exists bool
					_ = s.pool.QueryRow(r.Context(),
						`SELECT EXISTS(SELECT 1 FROM tags WHERE id = $1 AND organization_id = $2)`,
						id, scope.org.ID).Scan(&exists)
					if !exists {
						changeScope["changes.tags"] = append(changeScope["changes.tags"], "The selected tags is invalid.")
						break
					}
				}
				if len(changeScope["changes.tags"]) == 0 {
					changeTags = &tags
				}
			}
		}
	}
	if len(changeScope) > 0 {
		validationErr(w, changeScope)
		return
	}

	ids := make([]pgtype.UUID, 0, len(payload.IDs))
	valid := true
	for _, s := range payload.IDs {
		id, ok := domain.UUID(s)
		if !ok {
			valid = false
			continue
		}
		ids = append(ids, id)
	}
	_ = valid // malformed ids simply miss the org-scoped IN lookup → error list
	entries, err := s.timeEntriesByIDs(r.Context(), scope.org.ID, ids)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	byID := map[string]db.TimeEntry{}
	for _, e := range entries {
		byID[domain.UUIDString(e.ID)] = e
	}

	success := []string{}
	errList := []string{}
	for _, idStr := range payload.IDs {
		entry, found := byID[idStr]
		if !found {
			errList = append(errList, idStr)
			continue
		}
		if !canAccessAll && entry.MemberID != scope.member.ID {
			errList = append(errList, idStr)
			continue
		}
		resultingType := entry.Type
		if changeType != "" {
			resultingType = changeType
		}
		if resultingType == timeEntryTypeBreak && entry.Type != timeEntryTypeBreak && !scope.org.BreaksEnabled {
			errList = append(errList, idStr)
			continue
		}
		if resultingType == timeEntryTypeBreak && (projectChanged && changeProject.Valid || taskChanged && changeTask.Valid ||
			(changeBillable != nil && *changeBillable) || (changeTags != nil && len(*changeTags) > 0)) {
			errList = append(errList, idStr)
			continue
		}
		if changeMember != nil && !entry.End.Valid && changeMember.ID != entry.MemberID {
			errList = append(errList, idStr)
			continue
		}

		oldProject, oldTask := entry.ProjectID, entry.TaskID
		if changeStart != nil {
			entry.Start = ptts(*changeStart)
		}
		if raw, has := changes["end"]; has && string(raw) == "null" {
			entry.End = pgtype.Timestamp{}
		} else if changeEnd != nil {
			entry.End = ptts(*changeEnd)
		}
		if changeMember != nil {
			entry.MemberID = changeMember.ID
			entry.UserID = changeMember.UserID
		}
		if projectChanged {
			entry.ProjectID = changeProject
			entry.ClientID = changeClient
		}
		if taskChanged {
			entry.TaskID = changeTask
		}
		if projectChanged && !taskChanged && oldTask.Valid {
			var oldTaskProject pgtype.UUID
			if t, err := q.GetTaskByID(r.Context(), oldTask); err == nil {
				oldTaskProject = t.ProjectID
			}
			if oldTaskProject != entry.ProjectID {
				entry.TaskID = pgtype.UUID{}
			}
		}
		if changeBillable != nil {
			entry.Billable = *changeBillable
		}
		if changeType != "" {
			entry.Type = changeType
		}
		if changeDesc != nil {
			entry.Description = *changeDesc
		}
		if changeTags != nil {
			entry.Tags, _ = json.Marshal(*changeTags)
		}
		if entry.Type == timeEntryTypeBreak {
			entry.ProjectID = pgtype.UUID{}
			entry.TaskID = pgtype.UUID{}
			entry.ClientID = pgtype.UUID{}
			entry.Tags, _ = json.Marshal([]string{})
			entry.Billable = false
		}
		rate, err := domain.BillableRateForTimeEntry(r.Context(), q, scope.org.ID, entry.UserID, entry.ProjectID, entry.Billable)
		if err != nil {
			errList = append(errList, idStr)
			continue
		}
		entry.BillableRate = rate
		_, err = q.UpdateTimeEntry(r.Context(), db.UpdateTimeEntryParams{
			ID: entry.ID, Description: entry.Description, Start: entry.Start, End: entry.End,
			BillableRate: entry.BillableRate, Billable: entry.Billable, UserID: entry.UserID,
			ProjectID: entry.ProjectID, TaskID: entry.TaskID, Tags: entry.Tags,
			MemberID: entry.MemberID, ClientID: entry.ClientID, Type: entry.Type,
		})
		if err != nil {
			errList = append(errList, idStr)
			continue
		}
		_ = domain.RecalculateSpentTime(r.Context(), q, oldProject, oldTask)
		if oldProject != entry.ProjectID {
			_ = domain.RecalculateSpentTime(r.Context(), q, entry.ProjectID, pgtype.UUID{})
		}
		if oldTask != entry.TaskID {
			_ = domain.RecalculateSpentTime(r.Context(), q, pgtype.UUID{}, entry.TaskID)
		}
		success = append(success, idStr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": success, "error": errList})
}

func (s *Server) timeEntriesByIDs(ctx context.Context, orgID pgtype.UUID, ids []pgtype.UUID) ([]db.TimeEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+timeEntrySelectCols+` FROM time_entries WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
		orgID, uuidBytes(ids))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[db.TimeEntry])
}

// ---- destroy ----

func (s *Server) handleDestroyTimeEntry(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	entry, ok := s.scopedTimeEntry(w, r, scope)
	if !ok {
		return
	}
	if entry.MemberID == scope.member.ID {
		if !scope.has("time-entries:delete:own") {
			writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
			return
		}
	} else if !scope.has("time-entries:delete:all") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	q := db.New(s.pool)
	if err := q.DeleteTimeEntry(r.Context(), entry.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	_ = domain.RecalculateSpentTime(r.Context(), q, entry.ProjectID, entry.TaskID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDestroyMultipleTimeEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	if !scope.has("time-entries:delete:all") && !scope.has("time-entries:delete:own") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	canDeleteAll := scope.has("time-entries:delete:all")
	var payload struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.IDs) == 0 {
		validationErr(w, map[string][]string{"ids": {"The ids field is required."}})
		return
	}
	ids := make([]pgtype.UUID, 0, len(payload.IDs))
	for _, s := range payload.IDs {
		if id, ok := domain.UUID(s); ok {
			ids = append(ids, id)
		}
	}
	entries, err := s.timeEntriesByIDs(r.Context(), scope.org.ID, ids)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	byID := map[string]db.TimeEntry{}
	for _, e := range entries {
		byID[domain.UUIDString(e.ID)] = e
	}
	q := db.New(s.pool)
	success := []string{}
	errList := []string{}
	for _, idStr := range payload.IDs {
		entry, found := byID[idStr]
		if !found {
			errList = append(errList, idStr)
			continue
		}
		if !canDeleteAll && entry.MemberID != scope.member.ID {
			errList = append(errList, idStr)
			continue
		}
		if err := q.DeleteTimeEntry(r.Context(), entry.ID); err != nil {
			errList = append(errList, idStr)
			continue
		}
		_ = domain.RecalculateSpentTime(r.Context(), q, entry.ProjectID, entry.TaskID)
		success = append(success, idStr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": success, "error": errList})
}

// ---- my active ----

func (s *Server) handleMyActiveTimeEntry(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	entry, err := db.New(s.pool).RunningTimeEntryForUser(r.Context(), uid)
	if err != nil {
		writeMessage(w, http.StatusNotFound, "No active time entry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": timeEntryResource(entry)})
}
