package httpapi

// Dashboard chart endpoints — DashboardService port. All windows derive
// from the user's timezone and week_start setting, like upstream.

import (
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// lastDays returns the last n calendar dates (ascending) in loc — matches
// DashboardService::lastDays.
func lastDays(n int, loc *time.Location) []string {
	out := make([]string, 0, n)
	now := time.Now().In(loc)
	for i := n - 1; i >= 0; i-- {
		out = append(out, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return out
}

// daysOfThisWeek returns the 7 dates of the current week in loc, anchored
// on the user's week_start — DashboardService::daysOfThisWeek.
func daysOfThisWeek(loc *time.Location, weekStart string) []string {
	now := time.Now().In(loc)
	back := (int(now.Weekday()) - int(weekStartDay(weekStart)) + 7) % 7
	monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -back)
	out := make([]string, 0, 7)
	for i := 0; i < 7; i++ {
		out = append(out, monday.AddDate(0, 0, i).Format("2006-01-02"))
	}
	return out
}

// dateBounds converts the first/last possible dates to the UTC between
// window — constrainDateByPossibleDates.
func dateBounds(first, last string, loc *time.Location) (time.Time, time.Time) {
	a, _ := time.ParseInLocation("2006-01-02", first, loc)
	b, _ := time.ParseInLocation("2006-01-02", last, loc)
	if b.Before(a) {
		a, b = b, a
	}
	return a.UTC(), b.Add(24 * time.Hour).Add(-time.Second).UTC()
}

// currentWeekBounds ports constrainDateByCurrentWeek: startOfWeek UTC →
// endOfWeek UTC (start+7d-1s at second precision).
func currentWeekBounds(loc *time.Location, weekStart string) (time.Time, time.Time) {
	now := time.Now().In(loc)
	back := (int(now.Weekday()) - int(weekStartDay(weekStart)) + 7) % 7
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -back)
	return start.UTC(), start.AddDate(0, 0, 7).Add(-time.Second).UTC()
}

func (s *Server) handleWeeklyProjectOverview(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	loc := userTZ(u)
	start, end := currentWeekBounds(loc, u.WeekStart)
	q := db.New(s.pool)
	rows, err := q.WeeklyProjectOverview(r.Context(), db.WeeklyProjectOverviewParams{
		UserID: uid, OrganizationID: scope.org.ID,
		Column4: ptts(end), Column3: ptts(start),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	projIDs := []pgtype.UUID{}
	for _, row := range rows {
		if row.ProjectID.Valid {
			projIDs = append(projIDs, row.ProjectID)
		}
	}
	names := map[string]struct{ name, color string }{}
	if len(projIDs) > 0 {
		projs, err := q.ProjectNamesForChart(r.Context(), db.ProjectNamesForChartParams{
			OrganizationID: scope.org.ID, Column2: projIDs,
		})
		if err == nil {
			for _, p := range projs {
				names[domain.UUIDString(p.ID)] = struct{ name, color string }{p.Name, p.Color}
			}
		}
	}
	resp := []map[string]any{}
	other := int64(0)
	for _, row := range rows {
		pid := domain.UUIDString(row.ProjectID)
		if info, ok := names[pid]; ok {
			resp = append(resp, map[string]any{
				"value": row.Aggregate, "id": pid, "name": info.name, "color": info.color,
			})
		} else {
			other += row.Aggregate
		}
	}
	if other > 0 || len(resp) == 0 {
		resp = append(resp, map[string]any{
			"value": other, "id": nil, "name": "No project", "color": "#cccccc",
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLatestTasks(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	tasks, err := db.New(s.pool).LatestTasks(r.Context(), db.LatestTasksParams{
		OrganizationID: scope.org.ID, UserID: uid,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	resp := []map[string]any{}
	for _, t := range tasks {
		resp = append(resp, map[string]any{
			"id":           domain.UUIDString(t.ID),
			"name":         t.Name,
			"project_name": t.ProjectName,
			"project_id":   domain.UUIDString(t.TProjectID),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLastSevenDays(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	loc := userTZ(u)

	// lastDaysSplitInWindows(7, tz, 8) — 3h windows keyed by local date.
	now := time.Now().In(loc)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).
		AddDate(0, 0, 1).Add(-3 * time.Hour).UTC()
	type dayWin struct {
		date    string
		windows []string
	}
	days := []dayWin{}
	var seriesStart time.Time
	for i := 0; i < 7; i++ {
		dayLocal := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -i)
		dayUTC := dayLocal.UTC()
		seriesStart = dayUTC // last iteration wins, like upstream's overwrite
		wins := make([]string, 0, 8)
		for j := 0; j < 8; j++ {
			wins = append(wins, dayUTC.Add(time.Duration(j)*3*time.Hour).Format("2006-01-02 15:04:05"))
		}
		days = append(days, dayWin{date: dayLocal.Format("2006-01-02"), windows: wins})
	}
	rows, err := db.New(s.pool).LastSevenDaysWindows(r.Context(), db.LastSevenDaysWindowsParams{
		Column1: ptts(seriesStart), Column2: ptts(end),
		UserID: uid, OrganizationID: scope.org.ID, Column5: ptts(time.Now().UTC()),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	byWindow := map[string]int64{}
	for _, row := range rows {
		if t, ok := row.Start.(time.Time); ok {
			byWindow[t.UTC().Format("2006-01-02 15:04:05")] = row.Aggregate
		}
	}
	resp := []map[string]any{}
	for _, d := range days {
		history := make([]int64, 0, 8)
		var duration int64
		for _, win := range d.windows {
			v := byWindow[win]
			history = append(history, v)
			duration += v
		}
		resp = append(resp, map[string]any{
			"date": d.date, "duration": duration, "history": history,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLatestTeamActivity(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:all")
	if !ok {
		return
	}
	rows, err := db.New(s.pool).LatestTeamActivity(r.Context(), scope.org.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	// PHP sorts the per-member latest entries by start desc and takes 4.
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Start.Time.After(rows[j].Start.Time)
	})
	if len(rows) > 4 {
		rows = rows[:4]
	}
	resp := []map[string]any{}
	for _, row := range rows {
		resp = append(resp, map[string]any{
			"member_id":     domain.UUIDString(row.MemberID),
			"name":          row.UserName,
			"description":   row.Description,
			"time_entry_id": domain.UUIDString(row.ID),
			"task_id":       nullableUUID(row.TaskID),
			"status":        !row.End.Valid,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDailyTrackedHours(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	loc := userTZ(u)
	days := lastDays(100, loc)
	first, last := dateBounds(days[0], days[len(days)-1], loc)
	rows, err := db.New(s.pool).TimeEntriesForChartAggregate(r.Context(), db.TimeEntriesForChartAggregateParams{
		UserID: uid, Column2: int32(tzShift(loc)), OrganizationID: scope.org.ID,
		Start: ptts(first), Start_2: ptts(last),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	byDate := map[string]int64{}
	for _, row := range rows {
		if row.Date.Valid {
			byDate[row.Date.Time.Format("2006-01-02")] = row.Aggregate
		}
	}
	resp := []map[string]any{}
	for _, d := range days {
		resp = append(resp, map[string]any{"date": d, "duration": byDate[d]})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWeeklyHistory(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	loc := userTZ(u)
	days := daysOfThisWeek(loc, u.WeekStart)
	first, last := dateBounds(days[0], days[len(days)-1], loc)
	rows, err := db.New(s.pool).TimeEntriesForChartAggregate(r.Context(), db.TimeEntriesForChartAggregateParams{
		UserID: uid, Column2: int32(tzShift(loc)), OrganizationID: scope.org.ID,
		Start: ptts(first), Start_2: ptts(last),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	byDate := map[string]int64{}
	for _, row := range rows {
		if row.Date.Valid {
			byDate[row.Date.Time.Format("2006-01-02")] = row.Aggregate
		}
	}
	resp := []map[string]any{}
	for _, d := range days {
		resp = append(resp, map[string]any{"date": d, "duration": byDate[d]})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleTotalWeeklyTime(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	loc := userTZ(u)
	first, last := currentWeekBounds(loc, u.WeekStart)
	total, err := db.New(s.pool).TimeEntriesSumBetween(r.Context(), db.TimeEntriesSumBetweenParams{
		UserID: uid, OrganizationID: scope.org.ID,
		Column4: ptts(last), Column3: ptts(first),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, total)
}

func (s *Server) handleTotalWeeklyBillableTime(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	loc := userTZ(u)
	first, last := currentWeekBounds(loc, u.WeekStart)
	total, err := db.New(s.pool).TimeEntriesBillableSumBetween(r.Context(), db.TimeEntriesBillableSumBetweenParams{
		UserID: uid, OrganizationID: scope.org.ID,
		Column4: ptts(last), Column3: ptts(first),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, total)
}

func (s *Server) handleTotalWeeklyBillableAmount(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "charts:view:own")
	if !ok {
		return
	}
	if !canSeeBillableRates(scope) {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	loc := userTZ(u)
	first, last := currentWeekBounds(loc, u.WeekStart)
	total, err := db.New(s.pool).TimeEntriesBillableAmountBetween(r.Context(), db.TimeEntriesBillableAmountBetweenParams{
		UserID: uid, OrganizationID: scope.org.ID,
		Column4: ptts(last), Column3: ptts(first),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"value": total, "currency": scope.org.Currency,
	})
}
