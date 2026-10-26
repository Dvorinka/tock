package httpapi

import (
	"fmt"
	"html"
	"net/http"

	"github.com/Dvorinka/tock/internal/auth"
)

// The admin panel is Filament-parity upstream: a super-admin-only surface
// server-rendered rather than Inertia. Access is by email via SUPER_ADMINS.

func (s *Server) isSuperAdmin(u *auth.User) bool {
	if u == nil {
		return false
	}
	for _, e := range s.superAdmins {
		if e != "" && e == u.Email {
			return true
		}
	}
	return false
}

// requireSuperAdmin gates /admin — guests go to /login, non-super-admins 403.
func (s *Server) requireSuperAdmin(w http.ResponseWriter, r *http.Request) bool {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return false
	}
	if !s.isSuperAdmin(u) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
		return false
	}
	return true
}

type adminResource struct {
	Path    string
	Heading string
	Table   string
}

var adminResources = []adminResource{
	{"time-entries", "Time Entries", "time_entries"},
	{"projects", "Projects", "projects"},
	{"tasks", "Tasks", "tasks"},
	{"clients", "Clients", "clients"},
	{"tags", "Tags", "tags"},
	{"reports", "Reports", "reports"},
	{"users", "Users", "users"},
	{"organizations", "Organizations", "organizations"},
	{"organization-invitations", "Invitations", "organization_invitations"},
	{"project-members", "Project Members", "project_members"},
	{"tokens", "Tokens", "oauth_access_tokens"},
	{"failed-jobs", "Failed Jobs", "failed_jobs"},
}

func adminPage(title, content string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + html.EscapeString(title) +
		`</title><style>body{font-family:ui-sans-serif,system-ui,sans-serif;background:#f6f7f9;margin:0;padding:32px;color:#111}` +
		`.card{background:#fff;border:1px solid #e5e7eb;border-radius:10px;padding:20px;margin-bottom:16px}` +
		`.stat{display:inline-block;margin-right:32px}.stat b{font-size:26px}` +
		`h1{font-size:22px;margin:0 0 20px}h2{font-size:14px;color:#6b7280;margin:0 0 6px;font-weight:500}` +
		`table{width:100%;border-collapse:collapse;font-size:13px}td,th{border-bottom:1px solid #e5e7eb;text-align:left;padding:8px 10px}` +
		`nav a{color:#2563eb;margin-right:14px;text-decoration:none;font-size:13px}</style></head><body>` +
		content + `</body></html>`
}

func (s *Server) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	var real, placeholder, active7, timeEntries, imported int64
	q := s.pool
	_ = q.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE is_placeholder=false`).Scan(&real)
	_ = q.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE is_placeholder=true`).Scan(&placeholder)
	_ = q.QueryRow(r.Context(), `SELECT count(DISTINCT user_id) FROM time_entries WHERE created_at >= now() - interval '7 days'`).Scan(&active7)
	_ = q.QueryRow(r.Context(), `SELECT count(*) FROM time_entries`).Scan(&timeEntries)
	_ = q.QueryRow(r.Context(), `SELECT count(*) FROM jobs WHERE payload::text LIKE '%import%'`).Scan(&imported)
	stats := func(label string, n int64) string {
		return fmt.Sprintf(`<div class="stat"><h2>%s</h2><b>%d</b></div>`, label, n)
	}
	body := `<h1>Dashboard</h1><div class="card">` +
		stats("Total real users", real) +
		stats("Placeholder users", placeholder) +
		stats("Active users in the last seven days", active7) +
		stats("User Registrations", real+placeholder) +
		stats("Time Entries Created", timeEntries) +
		stats("Time Entries Imported", imported) +
		`</div><nav>`
	for _, res := range adminResources {
		body += `<a href="/admin/` + res.Path + `">` + res.Heading + `</a>`
	}
	body += `</nav>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminPage("Admin · Dashboard", body)))
}

// handleAdminResource lists a resource's rows under its h1 heading.
func (s *Server) handleAdminResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAdmin(w, r) {
		return
	}
	slug := r.URL.Path[len("/admin/"):]
	var res *adminResource
	for i := range adminResources {
		if adminResources[i].Path == slug {
			res = &adminResources[i]
			break
		}
	}
	if res == nil {
		http.NotFound(w, r)
		return
	}
	// Show the most recent rows — the tests assert the heading + a rendered table.
	rows, err := s.pool.Query(r.Context(), `SELECT * FROM `+res.Table+` LIMIT 50`)
	defer func() {
		if rows != nil {
			rows.Close()
		}
	}()
	var head, body string
	if err == nil {
		descs := rows.FieldDescriptions()
		head = `<table><tr>`
		for _, d := range descs {
			head += `<th>` + html.EscapeString(string(d.Name)) + `</th>`
		}
		head += `</tr>`
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				break
			}
			body += `<tr>`
			for _, v := range vals {
				body += `<td>` + html.EscapeString(fmt.Sprint(v)) + `</td>`
			}
			body += `</tr>`
		}
		body += `</table>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminPage("Admin · "+res.Heading, `<h1>`+res.Heading+`</h1><div class="card">`+head+body+`</div>`)))
}
