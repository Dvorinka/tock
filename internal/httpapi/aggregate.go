package httpapi

// Time-entry aggregation + dashboard charts + CSV exports.
// Ported from TimeEntryAggregationService, DashboardService, and the
// indexExport/aggregateExport endpoints of TimeEntryController.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

var aggregationTypes = map[string]bool{
	"day": true, "week": true, "month": true, "year": true,
	"user": true, "project": true, "task": true, "client": true,
	"billable": true, "description": true, "tag": true, "type": true,
}


// tzShift mirrors TimezoneService::getShiftFromUtc — the CURRENT offset of
// the user's timezone in seconds (upstream applies it uniformly).
func tzShift(loc *time.Location) int {
	_, off := time.Now().In(loc).Zone()
	return off
}

func shiftedStart(loc *time.Location) string {
	switch off := tzShift(loc); {
	case off > 0:
		return fmt.Sprintf("start + INTERVAL '%d second'", off)
	case off < 0:
		return fmt.Sprintf("start - INTERVAL '%d second'", -off)
	default:
		return "start"
	}
}

// carbonWeekDay maps the week_start string to Carbon's weekday numbering
// (Sunday=0 … Saturday=6), identical to Go's time.Weekday.
func weekStartDay(weekStart string) time.Weekday {
	switch weekStart {
	case "monday":
		return time.Monday
	case "tuesday":
		return time.Tuesday
	case "wednesday":
		return time.Wednesday
	case "thursday":
		return time.Thursday
	case "friday":
		return time.Friday
	case "saturday":
		return time.Saturday
	case "sunday":
		return time.Sunday
	default:
		return time.Monday
	}
}

// startOfWeekAnchor returns the current week's start (user's week_start) in
// the user's tz, formatted for the date_bin origin literal.
func startOfWeekAnchor(loc *time.Location, weekStart string) string {
	now := time.Now().In(loc)
	back := (int(now.Weekday()) - int(weekStartDay(weekStart)) + 7) % 7
	anchor := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -back)
	return anchor.Format("2006-01-02 15:04:05")
}

// groupByQuery ports getGroupByQuery — returns the SQL fragment producing
// the group key. All fragments derive from enum cases + validated values.
func groupByQuery(group, shifted, weekAnchor string) string {
	switch group {
	case "day":
		return "date(" + shifted + ")"
	case "week":
		return "to_char(date_bin('7 days', " + shifted + ", timestamp '" + weekAnchor + "'), 'YYYY-MM-DD')"
	case "month":
		return "to_char(" + shifted + ", 'YYYY-MM')"
	case "year":
		return "to_char(" + shifted + ", 'YYYY')"
	case "user":
		return "user_id::text"
	case "project":
		return "project_id::text"
	case "task":
		return "task_id::text"
	case "client":
		return "client_id::text"
	case "billable":
		return "billable::int"
	case "type":
		return "type"
	case "description":
		return "description"
	case "tag":
		return "tag"
	}
	return ""
}

// roundingSelects ports TimeEntryService::getStart/EndSelectRawForRounding.
// The 'now' literal is a fixed timestamp rendered at query time, like
// upstream's Carbon::now() interpolation.
func roundingSelects(roundingType string, roundingMinutes int, now string) (string, string) {
	endRaw := `coalesce("end", '` + now + `')`
	if roundingType == "" || roundingMinutes < 1 {
		return "start", endRaw
	}
	start := `date_bin('1 minutes', start, TIMESTAMP '1970-01-01')`
	switch roundingType {
	case "down":
		return start, fmt.Sprintf("date_bin('%d minutes', %s, %s)", roundingMinutes, endRaw, start)
	case "up":
		return start, fmt.Sprintf(
			"CASE WHEN %s = date_bin('%d minutes', %s, %s) THEN %s ELSE date_bin('%d minutes', %s + interval '%d minutes', %s) END",
			endRaw, roundingMinutes, endRaw, start, endRaw, roundingMinutes, endRaw, roundingMinutes, start)
	case "nearest":
		half := float64(roundingMinutes) / 2
		halfStr := strconv.FormatFloat(half, 'f', -1, 64)
		return start, fmt.Sprintf("date_bin('%d minutes', %s + interval '%s minutes', %s)", roundingMinutes, endRaw, halfStr, start)
	}
	return "start", endRaw
}

const tagLateralJoin = `CROSS JOIN LATERAL (
  SELECT jsonb_array_elements_text(coalesce(tags, '[]'::jsonb)) AS tag
  UNION ALL
  SELECT ''::text AS tag WHERE coalesce(jsonb_array_length(tags), 0) = 0
) AS tag(tag)`

type aggRow struct {
	g1     *string
	g2     *string
	agg    int64
	cost   int64
}

// runAggregateQuery executes the grouped aggregation and returns rows in
// group order, plus the non-tag-expanded base totals.
func (s *Server) runAggregateQuery(ctx context.Context, f *teFilter, g1Sel, g2Sel string, hasTag bool, startRaw, endRaw string) ([]aggRow, []aggRow, error) {
	sumExpr := "round(sum(extract(epoch from (" + endRaw + " - " + startRaw + "))))"
	costExpr := "round(sum(extract(epoch from (" + endRaw + " - " + startRaw + ")) * (coalesce(billable_rate, 0)::float/60/60)))"

	sel := ""
	groupBy := ""
	orderBy := ""
	if g1Sel != "" {
		sel = g1Sel + " AS group_1,"
		groupBy = " GROUP BY group_1"
		orderBy = " ORDER BY group_1"
		if g2Sel != "" {
			sel += g2Sel + " AS group_2,"
			groupBy = " GROUP BY group_1, group_2"
			orderBy = " ORDER BY group_1, group_2"
		}
	}
	join := ""
	if hasTag {
		join = " " + tagLateralJoin
	}
	sql := "SELECT " + sel + " " + sumExpr + " AS aggregate, " + costExpr + " AS cost" +
		" FROM time_entries" + join + " WHERE " + f.whereClause() + groupBy + orderBy

	rows, err := s.pool.Query(ctx, sql, f.args...)
	if err != nil {
		return nil, nil, err
	}
	var out []aggRow
	for rows.Next() {
		var g1, g2 *string
		var g1v, g2v any
		var agg, cost *int64
		dest := []any{}
		if g1Sel != "" {
			dest = append(dest, &g1v)
		}
		if g2Sel != "" {
			dest = append(dest, &g2v)
		}
		dest = append(dest, &agg, &cost)
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if g1Sel != "" {
			g1 = groupKeyString(g1v)
		}
		if g2Sel != "" {
			g2 = groupKeyString(g2v)
		}
		var a, c int64
		if agg != nil {
			a = *agg
		}
		if cost != nil {
			c = *cost
		}
		out = append(out, aggRow{g1: g1, g2: g2, agg: a, cost: c})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Base totals without tag expansion (for overall + per-group totals).
	var base []aggRow
	if hasTag || g1Sel == "" {
		baseSQL := "SELECT " + sel + " " + sumExpr + " AS aggregate, " + costExpr + " AS cost" +
			" FROM time_entries WHERE " + f.whereClause() + groupBy + orderBy
		brows, err := s.pool.Query(ctx, baseSQL, f.args...)
		if err != nil {
			return nil, nil, err
		}
		for brows.Next() {
			var g1v, g2v any
			var agg, cost *int64
			dest := []any{}
			if g1Sel != "" {
				dest = append(dest, &g1v)
			}
			if g2Sel != "" {
				dest = append(dest, &g2v)
			}
			dest = append(dest, &agg, &cost)
			if err := brows.Scan(dest...); err != nil {
				brows.Close()
				return nil, nil, err
			}
			var r aggRow
			if g1Sel != "" {
				r.g1 = groupKeyString(g1v)
			}
			if g2Sel != "" {
				r.g2 = groupKeyString(g2v)
			}
			if agg != nil {
				r.agg = *agg
			}
			if cost != nil {
				r.cost = *cost
			}
			base = append(base, r)
		}
		brows.Close()
		if err := brows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return out, base, nil
}

func groupKeyString(v any) *string {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		s := x
		return &s
	case []byte:
		s := string(x)
		return &s
	case time.Time:
		s := x.Format("2006-01-02")
		return &s
	case int32:
		s := strconv.Itoa(int(x))
		return &s
	case int64:
		s := strconv.FormatInt(x, 10)
		return &s
	case bool:
		if x {
			s := "1"
			return &s
		}
		s := "0"
		return &s
	default:
		s := fmt.Sprint(x)
		return &s
	}
}

// aggregate builds the upstream {seconds, cost, grouped_type, grouped_data}
// tree; descriptions are attached when withDescriptions is set (exports).
func (s *Server) aggregate(ctx context.Context, f *teFilter, group1, group2 string, loc *time.Location, weekStart string, fillGaps bool, start, end *time.Time, showBillableRate bool, roundingType string, roundingMinutes int, withDescriptions bool) (map[string]any, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	startRaw, endRaw := roundingSelects(roundingType, roundingMinutes, now)
	shifted := shiftedStart(loc)
	anchor := startOfWeekAnchor(loc, weekStart)

	var g1Sel, g2Sel string
	if group1 != "" {
		g1Sel = groupByQuery(group1, shifted, anchor)
		if group2 != "" {
			g2Sel = groupByQuery(group2, shifted, anchor)
		}
	}
	hasTag := group1 == "tag" || group2 == "tag"

	rows, base, err := s.runAggregateQuery(ctx, f, g1Sel, g2Sel, hasTag, startRaw, endRaw)
	if err != nil {
		return nil, err
	}

	var group1Response any
	var totalSeconds, totalCost int64

	if g1Sel != "" {
		// group rows by g1 preserving order
		type g1Bucket struct {
			key *string
			rows []aggRow
		}
		buckets := []g1Bucket{}
		bucketIdx := map[string]int{}
		for _, r := range rows {
			k := ""
			if r.g1 != nil {
				k = *r.g1
			}
			if idx, ok := bucketIdx[k]; ok {
				buckets[idx].rows = append(buckets[idx].rows, r)
			} else {
				bucketIdx[k] = len(buckets)
				buckets = append(buckets, g1Bucket{key: r.g1, rows: []aggRow{r}})
			}
		}
		baseByG1 := map[string]aggRow{}
		if group2 == "tag" {
			for _, b := range base {
				k := ""
				if b.g1 != nil {
					k = *b.g1
				}
				baseByG1[k] = b
			}
		}
		resp := []map[string]any{}
		for _, b := range buckets {
			var g2Response any
			var sum, cost int64
			if g2Sel != "" {
				g2list := []map[string]any{}
				for _, r := range b.rows {
					g2list = append(g2list, map[string]any{
						"key":          keyOrNull(r.g2),
						"seconds":      r.agg,
						"cost":         costOrNull(r.cost, showBillableRate),
						"grouped_type": nil,
						"grouped_data": nil,
					})
					sum += r.agg
					cost += r.cost
				}
				if group2 == "tag" {
					if bt, ok := baseByG1[keyStr(b.key)]; ok {
						sum, cost = bt.agg, bt.cost
					}
				}
				g2Response = g2list
			} else {
				sum = b.rows[0].agg
				cost = b.rows[0].cost
			}
			resp = append(resp, map[string]any{
				"key":          keyOrNull(b.key),
				"seconds":      sum,
				"cost":         costOrNull(cost, showBillableRate),
				"grouped_type": stringOrNil(group2),
				"grouped_data": g2Response,
			})
			totalSeconds += sum
			totalCost += cost
		}
		if hasTag {
			// Overall totals come from the non-expanded base query.
			totalSeconds, totalCost = 0, 0
			if len(base) > 0 {
				for _, b := range base {
					totalSeconds += b.agg
					totalCost += b.cost
				}
			}
		}
		group1Response = resp
		if fillGaps && start != nil && end != nil {
			group1Response = fillGapsInTimeGroups(resp, group1, group2, loc, weekStart, *start, *end)
		}
	} else {
		group1Response = nil
		if len(rows) > 0 {
			totalSeconds = rows[0].agg
			totalCost = rows[0].cost
		}
	}

	result := map[string]any{
		"seconds":      totalSeconds,
		"cost":         costOrNull(totalCost, showBillableRate),
		"grouped_type": stringOrNil(group1),
		"grouped_data": group1Response,
	}
	if withDescriptions {
		s.attachDescriptions(ctx, result, group1, group2)
	}
	return result, nil
}

func keyStr(k *string) string {
	if k == nil {
		return ""
	}
	return *k
}

func keyOrNull(k *string) any {
	if k == nil || *k == "" {
		return nil
	}
	return *k
}

func costOrNull(cost int64, show bool) any {
	if !show {
		return nil
	}
	return cost
}

func stringOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// timeSlotsBetween ports the slot generator: start is truncated to the
// interval in the user's tz, then advanced until reaching end.
func timeSlotsBetween(start, end time.Time, loc *time.Location, weekStart, interval, format string) []string {
	if start.After(end) {
		return nil
	}
	cur := start.In(loc)
	switch interval {
	case "day":
		cur = time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, loc)
	case "week":
		back := (int(cur.Weekday()) - int(weekStartDay(weekStart)) + 7) % 7
		cur = time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -back)
	case "month":
		cur = time.Date(cur.Year(), cur.Month(), 1, 0, 0, 0, 0, loc)
	case "year":
		cur = time.Date(cur.Year(), 1, 1, 0, 0, 0, 0, loc)
	}
	slots := []string{}
	for cur.Before(end.In(loc)) {
		slots = append(slots, cur.Format(format))
		switch interval {
		case "day":
			cur = cur.AddDate(0, 0, 1)
		case "week":
			cur = cur.AddDate(0, 0, 7)
		case "month":
			cur = cur.AddDate(0, 1, 0)
		case "year":
			cur = cur.AddDate(1, 0, 0)
		}
	}
	return slots
}

// fillGapsInTimeGroups ports the recursive gap filler for interval groups.
func fillGapsInTimeGroups(data []map[string]any, groupType, subGroupType string, loc *time.Location, weekStart string, start, end time.Time) []map[string]any {
	format, ok := map[string]string{"day": "2006-01-02", "week": "2006-01-02", "month": "2006-01", "year": "2006"}[groupType]
	if !ok {
		// Non-interval group: recurse into subgroups.
		for i, item := range data {
			sub, _ := item["grouped_data"].([]map[string]any)
			data[i]["grouped_data"] = fillGapsInTimeGroups(sub, subGroupType, "", loc, weekStart, start, end)
		}
		return data
	}
	slots := timeSlotsBetween(start, end, loc, weekStart, groupType, format)
	byKey := map[string]map[string]any{}
	for _, item := range data {
		if k, _ := item["key"].(string); k != "" {
			byKey[k] = item
		}
	}
	filled := make([]map[string]any, 0, len(slots))
	for _, slot := range slots {
		if found, ok := byKey[slot]; ok {
			entry := map[string]any{
				"key":          slot,
				"seconds":      found["seconds"],
				"cost":         found["cost"],
				"grouped_type": stringOrNil(subGroupType),
			}
			if subGroupType == "" {
				entry["grouped_data"] = nil
			} else {
				sub, _ := found["grouped_data"].([]map[string]any)
				entry["grouped_data"] = fillGapsInTimeGroups(sub, subGroupType, "", loc, weekStart, start, end)
			}
			filled = append(filled, entry)
		} else {
			entry := map[string]any{
				"key":          slot,
				"seconds":      0,
				"cost":         0,
				"grouped_type": stringOrNil(subGroupType),
			}
			if subGroupType == "" {
				entry["grouped_data"] = nil
			} else {
				entry["grouped_data"] = []map[string]any{}
			}
			filled = append(filled, entry)
		}
	}
	return filled
}

// attachDescriptions resolves group keys to {description, color} like
// getAggregatedTimeEntriesWithDescriptions.
func (s *Server) attachDescriptions(ctx context.Context, result map[string]any, group1, group2 string) {
	desc := func(keys []string, typ string) map[string][2]*string {
		out := map[string][2]*string{}
		if len(keys) == 0 {
			return out
		}
		switch typ {
		case "client", "user", "project", "task", "tag":
			table := map[string]string{"client": "clients", "user": "users", "project": "projects", "task": "tasks", "tag": "tags"}[typ]
			hasColor := typ == "project"
			sel := "id, name"
			if hasColor {
				sel += ", color"
			}
			rows, err := s.pool.Query(ctx, "SELECT "+sel+" FROM "+table+" WHERE id = ANY($1::uuid[])", keys)
			if err != nil {
				return out
			}
			for rows.Next() {
				var id, name string
				var color *string
				if hasColor {
					_ = rows.Scan(&id, &name, &color)
				} else {
					_ = rows.Scan(&id, &name)
				}
				n := name
				out[id] = [2]*string{&n, color}
			}
			rows.Close()
		case "billable":
			for _, k := range keys {
				var d string
				if k == "0" {
					d = "Non-billable"
				} else {
					d = "Billable"
				}
				out[k] = [2]*string{&d, nil}
			}
		case "type":
			for _, k := range keys {
				var d string
				if k == "break" {
					d = "Break"
				} else {
					d = "Work time"
				}
				out[k] = [2]*string{&d, nil}
			}
		case "description":
			for _, k := range keys {
				d := k
				out[k] = [2]*string{&d, nil}
			}
		}
		return out
	}

	gd, _ := result["grouped_data"].([]map[string]any)
	if gd == nil {
		return
	}
	keys1 := []string{}
	for _, item := range gd {
		if k, ok := item["key"].(string); ok {
			keys1 = append(keys1, k)
		}
	}
	map1 := desc(keys1, group1)
	for i, item := range gd {
		if k, ok := item["key"].(string); ok {
			if d, ok := map1[k]; ok {
				gd[i]["description"] = *d[0]
				gd[i]["color"] = d[1]
			}
		}
		sub, _ := item["grouped_data"].([]map[string]any)
		if sub != nil {
			keys2 := []string{}
			for _, s2 := range sub {
				if k, ok := s2["key"].(string); ok {
					keys2 = append(keys2, k)
				}
			}
			map2 := desc(keys2, group2)
			for j, s2 := range sub {
				if k, ok := s2["key"].(string); ok {
					if d, ok := map2[k]; ok {
						sub[j]["description"] = *d[0]
						sub[j]["color"] = d[1]
					}
				}
			}
			gd[i]["grouped_data"] = sub
		}
	}
	result["grouped_data"] = gd
}

// parseAggregateParams extracts group/sub_group/fill_gaps/rounding shared
// by aggregate + aggregateExport.
type aggParams struct {
	group1, group2   string
	fillGaps         bool
	roundingType     string
	roundingMinutes  int
	start, end       *time.Time
}

func parseAggregateParams(r *http.Request, requireGroup bool) (aggParams, map[string][]string) {
	p := aggParams{}
	errs := map[string][]string{}
	q := r.URL.Query()
	p.group1 = q.Get("group")
	p.group2 = q.Get("sub_group")
	if requireGroup && p.group1 == "" {
		errs["group"] = []string{"The group field is required."}
	}
	if p.group1 != "" && !aggregationTypes[p.group1] {
		errs["group"] = []string{"The selected group is invalid."}
	}
	if p.group2 != "" && !aggregationTypes[p.group2] {
		errs["sub_group"] = []string{"The selected sub group is invalid."}
	}
	if p.group1 == "" && p.group2 != "" {
		errs["sub_group"] = []string{"The sub group field is prohibited when group is not present."}
	}
	p.fillGaps = q.Get("fill_gaps_in_time_groups") == "true"
	if v := q.Get("rounding_type"); v != "" {
		if v == "up" || v == "down" || v == "nearest" {
			p.roundingType = v
		} else {
			errs["rounding_type"] = []string{"The selected rounding type is invalid."}
		}
	}
	if v := q.Get("rounding_minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			p.roundingMinutes = n
		} else {
			errs["rounding_minutes"] = []string{"The rounding minutes must be at least 1."}
		}
	}
	if (p.roundingType == "") != (p.roundingMinutes == 0) {
		errs["rounding_minutes"] = []string{"The rounding minutes field is required when rounding type is present."}
	}
	if v := q.Get("start"); v != "" {
		if t, ok := parseZulu(v); ok {
			p.start = &t
		} else {
			errs["start"] = []string{"The start does not match the format Y-m-d\\TH:i:s\\Z."}
		}
	}
	if v := q.Get("end"); v != "" {
		if t, ok := parseZulu(v); ok {
			p.end = &t
		} else {
			errs["end"] = []string{"The end does not match the format Y-m-d\\TH:i:s\\Z."}
		}
	}
	return p, errs
}

// memberFilterForView resolves the member_id query param + permission check
// shared by index/aggregate/export endpoints.
func (s *Server) memberFilterForView(w http.ResponseWriter, r *http.Request, scope *orgScope) (*db.Member, bool) {
	mid := r.URL.Query().Get("member_id")
	if mid == "" {
		if !scope.has("time-entries:view:all") {
			writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
			return nil, false
		}
		return nil, true
	}
	id, isUUID := domain.UUID(mid)
	m, err := db.New(s.pool).GetMemberByID(r.Context(), id)
	if !isUUID || err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Member", mid))
		return nil, false
	}
	if m.ID == scope.member.ID {
		if !scope.has("time-entries:view:own") {
			writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
			return nil, false
		}
	} else if !scope.has("time-entries:view:all") {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return nil, false
	}
	return &m, true
}

func (s *Server) handleAggregateTimeEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	memberFilter, ok := s.memberFilterForView(w, r, scope)
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	p, errs := parseAggregateParams(r, false)
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	f := s.buildTimeEntryWhere(r, scope.org.ID, memberFilter)
	result, err := s.aggregate(r.Context(), f, p.group1, p.group2, userTZ(u), u.WeekStart,
		p.fillGaps, p.start, p.end, canSeeBillableRates(scope), p.roundingType, p.roundingMinutes, false)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

// ---- exports ----

// exportCSV writes rows to storageDir/exports/<name>.csv and returns the
// signed download URL (upstream: temporaryUrl on the private disk, 5 min).
func (s *Server) exportCSV(r *http.Request, filename string, writeRows func(w *csv.Writer) error) (string, error) {
	dir := filepath.Join(s.storageDir, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, filename)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	cw := csv.NewWriter(f)
	if err := writeRows(cw); err != nil {
		cw.Flush()
		return "", err
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return "", err
	}
	base := filepath.Base(f.Name())
	expires := time.Now().Add(5 * time.Minute).Unix()
	sig := exportSignature(s.appKey, base, expires)
	return fmt.Sprintf("%s/exports/%s?expires=%d&signature=%s", s.appURL, base, expires, sig), nil
}

func hmacSHA256(key, data string) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func hmacEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

func exportSignature(appKey, file string, expires int64) string {
	mac := hmacSHA256(appKey, fmt.Sprintf("exports/%s|%d", file, expires))
	return fmt.Sprintf("%x", mac)
}

func (s *Server) handleExportDownload(w http.ResponseWriter, r *http.Request) {
	file := chi.URLParam(r, "file")
	if file == "" || file != filepath.Base(file) {
		writeMessage(w, http.StatusNotFound, "Not found")
		return
	}
	expires, _ := strconv.ParseInt(r.URL.Query().Get("expires"), 10, 64)
	sig := r.URL.Query().Get("signature")
	if expires == 0 || sig == "" || time.Now().Unix() > expires ||
		!hmacEqual(sig, exportSignature(s.appKey, file, expires)) {
		writeMessage(w, http.StatusForbidden, "Invalid signature.")
		return
	}
	http.ServeFile(w, r, filepath.Join(s.storageDir, "exports", file))
}

func (s *Server) handleExportTimeEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	memberFilter, ok := s.memberFilterForView(w, r, scope)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" {
		// xlsx/ods/pdf land in Phase 4; upstream would also 422 on unknown.
		validationErr(w, map[string][]string{"format": {"The selected format is invalid."}})
		return
	}
	u := auth.UserFromContext(r.Context())
	f := s.buildTimeEntryWhere(r, scope.org.ID, memberFilter)
	entries, err := s.queryTimeEntries(r.Context(), f, timeEntrySelectCols,
		`ORDER BY start DESC, id`, 0, 0)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	// Resolve display names like the export's eager relations.
	names := s.exportNames(r, entries)
	tz := userTZ(u)
	url, err := s.exportCSV(r, "time-entries-*.csv", func(cw *csv.Writer) error {
		if err := cw.Write([]string{"Description", "Task", "Project", "Client", "User", "Start", "End", "Duration", "Duration (decimal)", "Billable", "Break", "Tags"}); err != nil {
			return err
		}
		for _, e := range entries {
			end := ""
			dur := ""
			durDec := ""
			if e.End.Valid {
				end = e.End.Time.In(tz).Format("2006-01-02 15:04:05")
				secs := int64(e.End.Time.Sub(e.Start.Time).Seconds())
				dur = formatInterval(secs)
				durDec = strconv.FormatFloat(float64(secs)/3600, 'f', -1, 64)
			}
			billable := "No"
			if e.Billable {
				billable = "Yes"
			}
			brk := "No"
			if e.Type == timeEntryTypeBreak {
				brk = "Yes"
			}
			tagNames := []string{}
			var tagIDs []string
			_ = json.Unmarshal(e.Tags, &tagIDs)
			for _, t := range tagIDs {
				if n, ok := names.tags[t]; ok {
					tagNames = append(tagNames, n)
				}
			}
			if err := cw.Write([]string{
				e.Description,
				names.tasks[uuidStr(e.TaskID)],
				names.projects[uuidStr(e.ProjectID)],
				names.clients[uuidStr(e.ClientID)],
				names.users[uuidStr(e.UserID)],
				e.Start.Time.In(tz).Format("2006-01-02 15:04:05"),
				end, dur, durDec, billable, brk,
				strings.Join(tagNames, ", "),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"download_url": url})
}

type exportNameMaps struct {
	users    map[string]string
	projects map[string]string
	tasks    map[string]string
	clients  map[string]string
	tags     map[string]string
}

func uuidStr(id pgtype.UUID) string { return domain.UUIDString(id) }

func (s *Server) exportNames(r *http.Request, entries []db.TimeEntry) exportNameMaps {
	m := exportNameMaps{
		users: map[string]string{}, projects: map[string]string{},
		tasks: map[string]string{}, clients: map[string]string{},
		tags:  map[string]string{},
	}
	userIDs, projIDs, taskIDs, clientIDs, tagIDs := []string{}, []string{}, []string{}, []string{}, []string{}
	seen := map[string]bool{}
	add := func(list *[]string, id pgtype.UUID) {
		s := uuidStr(id)
		if s != "" && !seen[s] {
			seen[s] = true
			*list = append(*list, s)
		}
	}
	for _, e := range entries {
		add(&userIDs, e.UserID)
		add(&projIDs, e.ProjectID)
		add(&taskIDs, e.TaskID)
		add(&clientIDs, e.ClientID)
		var t []string
		_ = json.Unmarshal(e.Tags, &t)
		for _, x := range t {
			if !seen[x] {
				seen[x] = true
				tagIDs = append(tagIDs, x)
			}
		}
	}
	load := func(table string, ids []string, dst map[string]string) {
		if len(ids) == 0 {
			return
		}
		rows, err := s.pool.Query(r.Context(), "SELECT id::text, name FROM "+table+" WHERE id = ANY($1::uuid[])", ids)
		if err != nil {
			return
		}
		for rows.Next() {
			var id, name string
			_ = rows.Scan(&id, &name)
			dst[id] = name
		}
		rows.Close()
	}
	load("users", userIDs, m.users)
	load("projects", projIDs, m.projects)
	load("tasks", taskIDs, m.tasks)
	load("clients", clientIDs, m.clients)
	load("tags", tagIDs, m.tags)
	return m
}

// formatInterval ports IntervalService::format — floor(totalHours):MM:SS.
func formatInterval(seconds int64) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

func (s *Server) handleAggregateExportTimeEntries(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	memberFilter, ok := s.memberFilterForView(w, r, scope)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" {
		validationErr(w, map[string][]string{"format": {"The selected format is invalid."}})
		return
	}
	u := auth.UserFromContext(r.Context())
	p, errs := parseAggregateParams(r, true)
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	f := s.buildTimeEntryWhere(r, scope.org.ID, memberFilter)
	result, err := s.aggregate(r.Context(), f, p.group1, p.group2, userTZ(u), u.WeekStart,
		false, p.start, p.end, canSeeBillableRates(scope), p.roundingType, p.roundingMinutes, true)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	url, err := s.exportCSV(r, "report-*.csv", func(cw *csv.Writer) error {
		if err := cw.Write([]string{"Group", "Subgroup", "Description", "Seconds", "Cost"}); err != nil {
			return err
		}
		gd, _ := result["grouped_data"].([]map[string]any)
		var walk func(items []map[string]any, depth int) error
		walk = func(items []map[string]any, depth int) error {
			for _, item := range items {
				desc, _ := item["description"].(string)
				key, _ := item["key"].(string)
				if desc == "" {
					desc = key
				}
				cols := []string{"", "", desc,
					fmt.Sprint(item["seconds"]), fmt.Sprint(item["cost"])}
				if depth == 0 {
					cols[0] = fmt.Sprint(result["grouped_type"])
				} else {
					cols[1] = fmt.Sprint(item["grouped_type"])
				}
				if err := cw.Write(cols); err != nil {
					return err
				}
				if sub, ok := item["grouped_data"].([]map[string]any); ok && len(sub) > 0 {
					if err := walk(sub, depth+1); err != nil {
						return err
					}
				}
			}
			return nil
		}
		return walk(gd, 0)
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"download_url": url})
}
