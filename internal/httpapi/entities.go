package httpapi

// Clients, projects, project members, tags, tasks — CRUD ported verbatim
// from the upstream V1 controllers. Permission names match the Laravel
// PermissionStore matrix; the ":view:all" variants switch on the employee
// visibility scope (public projects or project membership).

import (
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Dvorinka/tock/internal/audit"
	"github.com/Dvorinka/tock/internal/auth"
	"github.com/Dvorinka/tock/internal/db"
	"github.com/Dvorinka/tock/internal/domain"
)

// archivedFilter maps the upstream 'archived'/'done' string filter
// (default 'false') to the sqlc mode: 0 = no filter, 1 = only active,
// 2 = only archived.
func archivedFilter(r *http.Request, param string) int32 {
	switch r.URL.Query().Get(param) {
	case "true":
		return 2
	case "false", "":
		return 1
	default:
		return 0
	}
}

func iso8601(t pgtype.Timestamp) string { return t.Time.UTC().Format("2006-01-02T15:04:05Z") }

// uniqueNameErr emits the upstream custom unique-name validation message.
func uniqueNameErr(w http.ResponseWriter, field, msg string) {
	validationErr(w, map[string][]string{field: {msg}})
}

var colorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// pgUUID bridges the string-returning newUUID to pgtype.UUID fields.
func pgUUID() pgtype.UUID {
	id, _ := domain.UUID(newUUID())
	return id
}

// ---- tags ----

func tagResource(t db.Tag) map[string]any {
	return map[string]any{
		"id":         domain.UUIDString(t.ID),
		"name":       t.Name,
		"created_at": iso8601(t.CreatedAt),
		"updated_at": iso8601(t.UpdatedAt),
	}
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "tags:view")
	if !ok {
		return
	}
	q := db.New(s.pool)
	page := pageParam(r)
	total, err := q.CountTags(r.Context(), scope.org.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	tags, err := q.ListTagsPaginated(r.Context(), db.ListTagsPaginatedParams{
		OrganizationID: scope.org.ID, Limit: defaultPerPage, Offset: int32((page - 1) * defaultPerPage),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		data = append(data, tagResource(t))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

func (s *Server) scopedTag(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.Tag, bool) {
	var tag db.Tag
	id, ok := domain.UUID(chi.URLParam(r, "tag"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Tag", chi.URLParam(r, "tag")))
		return tag, false
	}
	tag, err := db.New(s.pool).GetTagByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Tag", chi.URLParam(r, "tag")))
		return tag, false
	}
	if tag.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return tag, false
	}
	return tag, true
}

func (s *Server) tagNameUnique(r *http.Request, orgID pgtype.UUID, name string, excludeID pgtype.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM tags WHERE organization_id = $1 AND name = $2 AND ($3::uuid IS NULL OR id <> $3))`,
		orgID, name, excludeID).Scan(&exists)
	return !exists, err
}

func (s *Server) handleStoreTag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "tags:create")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		validationErr(w, map[string][]string{"name": {"The name field is required."}})
		return
	}
	if uniq, err := s.tagNameUnique(r, scope.org.ID, name, pgtype.UUID{}); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A tag with the same name already exists in the organization.")
		return
	}
	tag, err := db.New(s.pool).InsertTag(r.Context(), db.InsertTagParams{
		ID: pgUUID(), Name: name, OrganizationID: scope.org.ID,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	// Upstream returns the plain resource, so the status is 200 (only goals
	// set 201 explicitly).
	s.recordAudit(r, audit.EventCreated, audit.TypeTag, tag.ID, scope.org.ID, pgtype.UUID{}, nil, tagAttrs(tag))
	writeJSON(w, http.StatusOK, map[string]any{"data": tagResource(tag)})
}

func (s *Server) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "tags:update")
	if !ok {
		return
	}
	tag, ok := s.scopedTag(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		validationErr(w, map[string][]string{"name": {"The name field is required."}})
		return
	}
	if uniq, err := s.tagNameUnique(r, scope.org.ID, name, tag.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A tag with the same name already exists in the organization.")
		return
	}
	auditBefore := tagAttrs(tag)
	tag, err := db.New(s.pool).UpdateTagName(r.Context(), db.UpdateTagNameParams{ID: tag.ID, Name: name})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventUpdated, audit.TypeTag, tag.ID, scope.org.ID, pgtype.UUID{}, auditBefore, tagAttrs(tag))
	writeJSON(w, http.StatusOK, map[string]any{"data": tagResource(tag)})
}

func (s *Server) handleDestroyTag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "tags:delete")
	if !ok {
		return
	}
	tag, ok := s.scopedTag(w, r, scope)
	if !ok {
		return
	}
	inUse, err := db.New(s.pool).TimeEntryExistsWithTag(r.Context(), db.TimeEntryExistsWithTagParams{
		OrganizationID: scope.org.ID, Column2: domain.UUIDString(tag.ID),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if inUse {
		writeDomainErr(w, domain.ErrEntityStillInUse("tag", "time_entry"))
		return
	}
	if err := db.New(s.pool).DeleteTag(r.Context(), tag.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeTag, tag.ID, scope.org.ID, pgtype.UUID{}, tagAttrs(tag), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- clients ----

func clientResource(c db.Client) map[string]any {
	return map[string]any{
		"id":         domain.UUIDString(c.ID),
		"name":       c.Name,
		"is_archived": c.ArchivedAt.Valid,
		"created_at": iso8601(c.CreatedAt),
		"updated_at": iso8601(c.UpdatedAt),
	}
}

func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "clients:view")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	employeeOnly := !scope.has("clients:view:all")
	arch := archivedFilter(r, "archived")
	q := db.New(s.pool)
	page := pageParam(r)
	params := db.ListClientsPaginatedParams{
		OrganizationID: scope.org.ID, Column2: arch, Column3: employeeOnly,
		UserID: uid, Limit: defaultPerPage, Offset: int32((page - 1) * defaultPerPage),
	}
	total, err := q.CountClients(r.Context(), db.CountClientsParams{
		OrganizationID: scope.org.ID, Column2: arch, Column3: employeeOnly, UserID: uid,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	clients, err := q.ListClientsPaginated(r.Context(), params)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		data = append(data, clientResource(c))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

func (s *Server) scopedClient(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.Client, bool) {
	var client db.Client
	id, ok := domain.UUID(chi.URLParam(r, "client"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Client", chi.URLParam(r, "client")))
		return client, false
	}
	client, err := db.New(s.pool).GetClientByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Client", chi.URLParam(r, "client")))
		return client, false
	}
	if client.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return client, false
	}
	return client, true
}

func (s *Server) clientNameUnique(r *http.Request, orgID pgtype.UUID, name string, excludeID pgtype.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM clients WHERE organization_id = $1 AND name = $2 AND ($3::uuid IS NULL OR id <> $3))`,
		orgID, name, excludeID).Scan(&exists)
	return !exists, err
}

func (s *Server) handleStoreClient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "clients:create")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		validationErr(w, map[string][]string{"name": {"The name field is required."}})
		return
	}
	if uniq, err := s.clientNameUnique(r, scope.org.ID, name, pgtype.UUID{}); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A client with the same name already exists in the organization.")
		return
	}
	client, err := db.New(s.pool).InsertClient(r.Context(), db.InsertClientParams{
		ID: pgUUID(), Name: name, OrganizationID: scope.org.ID,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventCreated, audit.TypeClient, client.ID, scope.org.ID, pgtype.UUID{}, nil, clientAttrs(client))
	writeJSON(w, http.StatusOK, map[string]any{"data": clientResource(client)})
}

func (s *Server) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "clients:update")
	if !ok {
		return
	}
	client, ok := s.scopedClient(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		validationErr(w, map[string][]string{"name": {"The name field is required."}})
		return
	}
	if uniq, err := s.clientNameUnique(r, scope.org.ID, name, client.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A client with the same name already exists in the organization.")
		return
	}
	archived := client.ArchivedAt
	if _, has := body["is_archived"]; has {
		b, _, bok := jsonBool(body, "is_archived")
		if !bok {
			validationErr(w, map[string][]string{"is_archived": {"The is archived field must be true or false."}})
			return
		}
		if b {
			archived = pgtype.Timestamp{Time: time.Now().UTC(), Valid: true}
		} else {
			archived = pgtype.Timestamp{}
		}
	}
	auditBefore := clientAttrs(client)
	client, err := db.New(s.pool).UpdateClient(r.Context(), db.UpdateClientParams{
		ID: client.ID, Name: name, ArchivedAt: archived,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventUpdated, audit.TypeClient, client.ID, scope.org.ID, pgtype.UUID{}, auditBefore, clientAttrs(client))
	writeJSON(w, http.StatusOK, map[string]any{"data": clientResource(client)})
}

func (s *Server) handleDestroyClient(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "clients:delete")
	if !ok {
		return
	}
	client, ok := s.scopedClient(w, r, scope)
	if !ok {
		return
	}
	hasProjects, err := db.New(s.pool).ClientHasProjects(r.Context(), client.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if hasProjects {
		writeDomainErr(w, domain.ErrEntityStillInUse("client", "project"))
		return
	}
	if err := db.New(s.pool).DeleteClient(r.Context(), client.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeClient, client.ID, scope.org.ID, pgtype.UUID{}, clientAttrs(client), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- projects ----

func projectResource(p db.Project, showBillableRate bool) map[string]any {
	var rate any
	if showBillableRate && p.BillableRate != nil {
		rate = *p.BillableRate
	}
	var estimated any
	if p.EstimatedTime != nil {
		estimated = *p.EstimatedTime
	}
	return map[string]any{
		"id":            domain.UUIDString(p.ID),
		"name":          p.Name,
		"color":         p.Color,
		"client_id":     nullableUUID(p.ClientID),
		"is_archived":   p.ArchivedAt.Valid,
		"billable_rate": rate,
		"is_billable":   p.IsBillable,
		"estimated_time": estimated,
		"spent_time":    p.SpentTime,
		"is_public":     p.IsPublic,
		"created_at":    iso8601(p.CreatedAt),
		"updated_at":    iso8601(p.UpdatedAt),
	}
}

func nullableUUID(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return domain.UUIDString(id)
}

func canSeeBillableRates(scope *orgScope) bool {
	// BillableRateService::canSeeBillableRates — billing contract is always
	// true here (no gates), so it reduces to the role/settings check.
	return deref(scope.member.Role) != domain.RoleEmployee || scope.org.EmployeesCanSeeBillableRates
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "projects:view")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	employeeOnly := !scope.has("projects:view:all")
	arch := archivedFilter(r, "archived")
	q := db.New(s.pool)
	page := pageParam(r)
	total, err := q.CountProjects(r.Context(), db.CountProjectsParams{
		OrganizationID: scope.org.ID, Column2: arch, Column3: employeeOnly, UserID: uid,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	projects, err := q.ListProjectsPaginated(r.Context(), db.ListProjectsPaginatedParams{
		OrganizationID: scope.org.ID, Column2: arch, Column3: employeeOnly,
		UserID: uid, Limit: defaultPerPage, Offset: int32((page - 1) * defaultPerPage),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	showRate := canSeeBillableRates(scope)
	data := make([]map[string]any, 0, len(projects))
	for _, p := range projects {
		data = append(data, projectResource(p, showRate))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

func (s *Server) scopedProject(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.Project, bool) {
	var project db.Project
	id, ok := domain.UUID(chi.URLParam(r, "project"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Project", chi.URLParam(r, "project")))
		return project, false
	}
	project, err := db.New(s.pool).GetProjectByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Project", chi.URLParam(r, "project")))
		return project, false
	}
	if project.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return project, false
	}
	return project, true
}

func (s *Server) handleShowProject(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "projects:view:all")
	if !ok {
		return
	}
	project, ok := s.scopedProject(w, r, scope)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": projectResource(project, true)})
}

func (s *Server) projectNameUnique(r *http.Request, orgID, clientID pgtype.UUID, name string, excludeID pgtype.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM projects WHERE organization_id = $1 AND name = $2
		   AND client_id IS NOT DISTINCT FROM $3 AND ($4::uuid IS NULL OR id <> $4))`,
		orgID, name, clientID, excludeID).Scan(&exists)
	return !exists, err
}

func (s *Server) handleStoreProject(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "projects:create")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	errs := map[string][]string{}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		errs["name"] = []string{"The name field is required."}
	}
	color, present, valid := jsonStr(body, "color")
	if !present || !valid || len(color) > 255 || !colorRe.MatchString(color) {
		errs["color"] = []string{"The color field must be a valid color."}
	}
	isBillable, bPresent, bValid := jsonBool(body, "is_billable")
	if !bPresent || !bValid {
		errs["is_billable"] = []string{"The is billable field is required."}
	}
	// client_id is 'present|nullable' + exists in org
	var clientID pgtype.UUID
	clientRaw, clientPresent := body["client_id"]
	if !clientPresent {
		errs["client_id"] = []string{"The client id field is required."}
	} else if string(clientRaw) != "null" {
		var cid string
		if err := json.Unmarshal(clientRaw, &cid); err != nil {
			errs["client_id"] = []string{"The client id must be a valid UUID."}
		} else if id, ok := domain.UUID(cid); !ok {
			errs["client_id"] = []string{"The client id must be a valid UUID."}
		} else if _, err := db.New(s.pool).GetClientByID(r.Context(), id); err != nil {
			errs["client_id"] = []string{"The selected client id is invalid."}
		} else if c, _ := db.New(s.pool).GetClientByID(r.Context(), id); c.OrganizationID != scope.org.ID {
			errs["client_id"] = []string{"The selected client id is invalid."}
		} else {
			clientID = id
		}
	}
	billableRate, ratePresent, rateValid := jsonNullableInt(body, "billable_rate")
	if ratePresent && !rateValid {
		errs["billable_rate"] = []string{"The billable rate must be an integer."}
	}
	estimated, estPresent, estValid := jsonNullableInt(body, "estimated_time")
	if estPresent && !estValid {
		errs["estimated_time"] = []string{"The estimated time must be an integer."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	if uniq, err := s.projectNameUnique(r, scope.org.ID, clientID, name, pgtype.UUID{}); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A project with the same name and client already exists in the organization.")
		return
	}
	isPublic, _, _ := jsonBool(body, "is_public")
	project, err := db.New(s.pool).InsertProject(r.Context(), db.InsertProjectParams{
		ID: pgUUID(), Name: name, Color: color,
		BillableRate: billableRate, IsPublic: isPublic, ClientID: clientID,
		OrganizationID: scope.org.ID, IsBillable: isBillable,
		ArchivedAt: pgtype.Timestamp{}, EstimatedTime: estimated,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventCreated, audit.TypeProject, project.ID, scope.org.ID, pgtype.UUID{}, nil, projectAttrs(project))
	writeJSON(w, http.StatusOK, map[string]any{"data": projectResource(project, true)})
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "projects:update")
	if !ok {
		return
	}
	project, ok := s.scopedProject(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	errs := map[string][]string{}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		errs["name"] = []string{"The name field is required."}
	}
	color, present, valid := jsonStr(body, "color")
	if !present || !valid || len(color) > 255 || !colorRe.MatchString(color) {
		errs["color"] = []string{"The color field must be a valid color."}
	}
	if _, has := body["is_billable"]; has {
		if _, _, bok := jsonBool(body, "is_billable"); !bok {
			errs["is_billable"] = []string{"The is billable field must be true or false."}
		}
	}
	var clientID pgtype.UUID
	clientRaw, clientPresent := body["client_id"]
	if !clientPresent {
		errs["client_id"] = []string{"The client id field is required."}
	} else if string(clientRaw) != "null" {
		var cid string
		if err := json.Unmarshal(clientRaw, &cid); err != nil {
			errs["client_id"] = []string{"The client id must be a valid UUID."}
		} else if id, ok := domain.UUID(cid); !ok {
			errs["client_id"] = []string{"The client id must be a valid UUID."}
		} else if c, err := db.New(s.pool).GetClientByID(r.Context(), id); err != nil || c.OrganizationID != scope.org.ID {
			errs["client_id"] = []string{"The selected client id is invalid."}
		} else {
			clientID = id
		}
	}
	billableRate, ratePresent, rateValid := jsonNullableInt(body, "billable_rate")
	if ratePresent && !rateValid {
		errs["billable_rate"] = []string{"The billable rate must be an integer."}
	}
	estimated, estPresent, estValid := jsonNullableInt(body, "estimated_time")
	if estPresent && !estValid {
		errs["estimated_time"] = []string{"The estimated time must be an integer."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	if uniq, err := s.projectNameUnique(r, scope.org.ID, clientID, name, project.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A project with the same name and client already exists in the organization.")
		return
	}

	oldRate := project.BillableRate
	clientChanged := project.ClientID != clientID
	if b, has, bok := jsonBool(body, "is_billable"); has && bok {
		project.IsBillable = b
	}
	if _, has := body["is_archived"]; has {
		b, _, bok := jsonBool(body, "is_archived")
		if !bok {
			validationErr(w, map[string][]string{"is_archived": {"The is archived field must be true or false."}})
			return
		}
		if b {
			project.ArchivedAt = pgtype.Timestamp{Time: time.Now().UTC(), Valid: true}
		} else {
			project.ArchivedAt = pgtype.Timestamp{}
		}
	}
	if _, has := body["is_public"]; has {
		b, _, bok := jsonBool(body, "is_public")
		if !bok {
			validationErr(w, map[string][]string{"is_public": {"The is public field must be true or false."}})
			return
		}
		project.IsPublic = b
	}
	if _, has := body["estimated_time"]; has {
		project.EstimatedTime = estimated
	}
	if ratePresent {
		project.BillableRate = billableRate
	}
	project.Name = name
	project.Color = color
	project.ClientID = clientID

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := db.New(s.pool).WithTx(tx)
	auditBefore := projectAttrs(project)
	project, err = q.UpdateProject(r.Context(), db.UpdateProjectParams{
		ID: project.ID, Name: project.Name, Color: project.Color,
		BillableRate: project.BillableRate, IsPublic: project.IsPublic,
		ClientID: project.ClientID, IsBillable: project.IsBillable,
		ArchivedAt: project.ArchivedAt, EstimatedTime: project.EstimatedTime,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rateChanged := (oldRate == nil) != (project.BillableRate == nil) ||
		(oldRate != nil && project.BillableRate != nil && *oldRate != *project.BillableRate)
	if rateChanged {
		if err := q.PropagateBillableRateForProject(r.Context(), db.PropagateBillableRateForProjectParams{
			OrganizationID: scope.org.ID, ProjectID: project.ID, BillableRate: project.BillableRate,
		}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if clientChanged {
		if err := q.UpdateTimeEntriesClientForProject(r.Context(), db.UpdateTimeEntriesClientForProjectParams{
			OrganizationID: scope.org.ID, ProjectID: project.ID, ClientID: clientID,
		}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventUpdated, audit.TypeProject, project.ID, scope.org.ID, pgtype.UUID{}, auditBefore, projectAttrs(project))
	writeJSON(w, http.StatusOK, map[string]any{"data": projectResource(project, true)})
}

func (s *Server) handleDestroyProject(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "projects:delete")
	if !ok {
		return
	}
	project, ok := s.scopedProject(w, r, scope)
	if !ok {
		return
	}
	q := db.New(s.pool)
	hasTasks, err := q.ProjectHasTasks(r.Context(), project.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if hasTasks {
		writeDomainErr(w, domain.ErrEntityStillInUse("project", "task"))
		return
	}
	hasEntries, err := q.ProjectHasTimeEntries(r.Context(), project.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if hasEntries {
		writeDomainErr(w, domain.ErrEntityStillInUse("project", "time_entry"))
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	tq := q.WithTx(tx)
	if err := tq.DeleteProjectMembersForProject(r.Context(), project.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if err := tq.DeleteProject(r.Context(), project.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeProject, project.ID, scope.org.ID, pgtype.UUID{}, projectAttrs(project), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- project members ----

func projectMemberResource(pm db.ProjectMember) map[string]any {
	var rate any
	if pm.BillableRate != nil {
		rate = *pm.BillableRate
	}
	return map[string]any{
		"id":            domain.UUIDString(pm.ID),
		"billable_rate": rate,
		"member_id":     domain.UUIDString(pm.MemberID),
		"project_id":    domain.UUIDString(pm.ProjectID),
	}
}

func (s *Server) handleListProjectMembers(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "project-members:view")
	if !ok {
		return
	}
	project, ok := s.scopedProject(w, r, scope)
	if !ok {
		return
	}
	q := db.New(s.pool)
	page := pageParam(r)
	total, err := q.CountProjectMembers(r.Context(), project.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	members, err := q.ListProjectMembersPaginated(r.Context(), db.ListProjectMembersPaginatedParams{
		ProjectID: project.ID, Limit: defaultPerPage, Offset: int32((page - 1) * defaultPerPage),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := make([]map[string]any, 0, len(members))
	for _, m := range members {
		data = append(data, projectMemberResource(m))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

func (s *Server) handleStoreProjectMember(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "project-members:create")
	if !ok {
		return
	}
	project, ok := s.scopedProject(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	memberIDStr, present, valid := jsonStr(body, "member_id")
	memberID, isUUID := domain.UUID(memberIDStr)
	if !present || !valid || !isUUID {
		validationErr(w, map[string][]string{"member_id": {"The member id field is required."}})
		return
	}
	billableRate, ratePresent, rateValid := jsonNullableInt(body, "billable_rate")
	if ratePresent && !rateValid {
		validationErr(w, map[string][]string{"billable_rate": {"The billable rate must be an integer."}})
		return
	}
	q := db.New(s.pool)
	member, err := q.GetMemberByID(r.Context(), memberID)
	if err != nil || member.OrganizationID != scope.org.ID {
		validationErr(w, map[string][]string{"member_id": {"The selected member id is invalid."}})
		return
	}
	var isPlaceholder bool
	if err := s.pool.QueryRow(r.Context(),
		`SELECT is_placeholder FROM users WHERE id = $1`, member.UserID).Scan(&isPlaceholder); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if isPlaceholder {
		writeDomainErr(w, domain.ErrInactiveUser)
		return
	}
	exists, err := q.ProjectMemberExists(r.Context(), db.ProjectMemberExistsParams{
		ProjectID: project.ID, MemberID: memberID,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if exists {
		writeDomainErr(w, domain.ErrUserAlreadyProjectMember)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	tq := q.WithTx(tx)
	pm, err := tq.InsertProjectMember(r.Context(), db.InsertProjectMemberParams{
		ID: pgUUID(), BillableRate: billableRate, ProjectID: project.ID,
		UserID: member.UserID, MemberID: memberID,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if pm.BillableRate != nil {
		if err := tq.PropagateBillableRateForProjectMember(r.Context(), db.PropagateBillableRateForProjectMemberParams{
			MemberID: pm.MemberID, ProjectID: pm.ProjectID, BillableRate: pm.BillableRate,
		}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventCreated, audit.TypeProjectMember, pm.ID, scope.org.ID, pgtype.UUID{}, nil, projectMemberAttrs(pm))
	writeJSON(w, http.StatusOK, map[string]any{"data": projectMemberResource(pm)})
}

// scopedProjectMember loads {projectMember} and verifies it belongs to the org
// through its project — Laravel route binding + org check.
func (s *Server) scopedProjectMember(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.GetProjectMemberByIDRow, bool) {
	var row db.GetProjectMemberByIDRow
	id, ok := domain.UUID(chi.URLParam(r, "projectMember"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("ProjectMember", chi.URLParam(r, "projectMember")))
		return row, false
	}
	row, err := db.New(s.pool).GetProjectMemberByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("ProjectMember", chi.URLParam(r, "projectMember")))
		return row, false
	}
	if row.ProjectOrgID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return row, false
	}
	return row, true
}

func (s *Server) handleUpdateProjectMember(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "project-members:update")
	if !ok {
		return
	}
	row, ok := s.scopedProjectMember(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	billableRate, ratePresent, rateValid := jsonNullableInt(body, "billable_rate")
	if ratePresent && !rateValid {
		validationErr(w, map[string][]string{"billable_rate": {"The billable rate must be an integer."}})
		return
	}
	auditBefore := projectMemberRowAttrs(row)
	oldRate := row.BillableRate
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	tq := db.New(s.pool).WithTx(tx)
	pm, err := tq.UpdateProjectMemberRate(r.Context(), db.UpdateProjectMemberRateParams{
		ID: row.ID, BillableRate: billableRate,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	rateChanged := (oldRate == nil) != (pm.BillableRate == nil) ||
		(oldRate != nil && pm.BillableRate != nil && *oldRate != *pm.BillableRate)
	if rateChanged {
		if err := tq.PropagateBillableRateForProjectMember(r.Context(), db.PropagateBillableRateForProjectMemberParams{
			MemberID: pm.MemberID, ProjectID: pm.ProjectID, BillableRate: pm.BillableRate,
		}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventUpdated, audit.TypeProjectMember, pm.ID, scope.org.ID, pgtype.UUID{}, auditBefore, projectMemberAttrs(pm))
	writeJSON(w, http.StatusOK, map[string]any{"data": projectMemberResource(pm)})
}

func (s *Server) handleDestroyProjectMember(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "project-members:delete")
	if !ok {
		return
	}
	row, ok := s.scopedProjectMember(w, r, scope)
	if !ok {
		return
	}
	q := db.New(s.pool)
	hadRate := row.BillableRate != nil
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	tq := q.WithTx(tx)
	if err := tq.DeleteProjectMember(r.Context(), row.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if hadRate {
		member, err := tq.GetMemberByID(r.Context(), row.MemberID)
		if err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		project, err := tq.GetProjectByID(r.Context(), row.ProjectID)
		if err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		if err := tq.PropagateBillableRateForMember(r.Context(), db.PropagateBillableRateForMemberParams{
			MemberID: member.ID, BillableRate: member.BillableRate,
		}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		if err := tq.PropagateBillableRateForProject(r.Context(), db.PropagateBillableRateForProjectParams{
			OrganizationID: scope.org.ID, ProjectID: project.ID, BillableRate: project.BillableRate,
		}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
		if err := tq.PropagateBillableRateForOrganization(r.Context(), db.PropagateBillableRateForOrganizationParams{
			OrganizationID: scope.org.ID, BillableRate: scope.org.BillableRate,
		}); err != nil {
			writeMessage(w, http.StatusInternalServerError, "server error")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeProjectMember, row.ID, scope.org.ID, pgtype.UUID{}, projectMemberRowAttrs(row), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- tasks ----

func taskResource(t db.Task) map[string]any {
	var estimated any
	if t.EstimatedTime != nil {
		estimated = *t.EstimatedTime
	}
	return map[string]any{
		"id":             domain.UUIDString(t.ID),
		"name":           t.Name,
		"is_done":        t.DoneAt.Valid,
		"project_id":     domain.UUIDString(t.ProjectID),
		"estimated_time": estimated,
		"spent_time":     t.SpentTime,
		"created_at":     iso8601(t.CreatedAt),
		"updated_at":     iso8601(t.UpdatedAt),
	}
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "tasks:view")
	if !ok {
		return
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	employeeOnly := !scope.has("tasks:view:all")
	var projectID pgtype.UUID
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		projectID, _ = domain.UUID(pid)
	}
	done := archivedFilter(r, "done")
	q := db.New(s.pool)
	page := pageParam(r)
	total, err := q.CountTasks(r.Context(), db.CountTasksParams{
		OrganizationID: scope.org.ID, Column2: done, Column3: projectID,
		Column4: employeeOnly, UserID: uid,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	tasks, err := q.ListTasksPaginated(r.Context(), db.ListTasksPaginatedParams{
		OrganizationID: scope.org.ID, Column2: done, Column3: projectID,
		Column4: employeeOnly, UserID: uid,
		Limit: defaultPerPage, Offset: int32((page - 1) * defaultPerPage),
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	data := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		data = append(data, taskResource(t))
	}
	paginatedResponse(w, r, data, total, page, defaultPerPage)
}

func (s *Server) scopedTask(w http.ResponseWriter, r *http.Request, scope *orgScope) (db.Task, bool) {
	var task db.Task
	id, ok := domain.UUID(chi.URLParam(r, "task"))
	if !ok {
		writeMessage(w, http.StatusNotFound, modelNotFound("Task", chi.URLParam(r, "task")))
		return task, false
	}
	task, err := db.New(s.pool).GetTaskByID(r.Context(), id)
	if err != nil {
		writeMessage(w, http.StatusNotFound, modelNotFound("Task", chi.URLParam(r, "task")))
		return task, false
	}
	if task.OrganizationID != scope.org.ID {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return task, false
	}
	return task, true
}

func (s *Server) taskNameUnique(r *http.Request, projectID pgtype.UUID, name string, excludeID pgtype.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id = $1 AND name = $2 AND ($3::uuid IS NULL OR id <> $3))`,
		projectID, name, excludeID).Scan(&exists)
	return !exists, err
}

// checkScopedTaskPermission ports checkScopedPermissionForProject: the base
// permission must hold AND the project must be visible to the employee.
func (s *Server) checkScopedTaskPermission(w http.ResponseWriter, r *http.Request, scope *orgScope, projectID pgtype.UUID, perm string) bool {
	if !scope.has(perm) {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return false
	}
	u := auth.UserFromContext(r.Context())
	uid, _ := domain.UUID(u.ID)
	visible, err := db.New(s.pool).ProjectVisibleToUser(r.Context(), db.ProjectVisibleToUserParams{
		ID: projectID, OrganizationID: scope.org.ID, UserID: uid,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return false
	}
	if !visible {
		writeMessage(w, http.StatusForbidden, "This action is unauthorized.")
		return false
	}
	return true
}

func (s *Server) handleStoreTask(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	errs := map[string][]string{}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		errs["name"] = []string{"The name field is required."}
	}
	projectIDStr, pPresent, pValid := jsonStr(body, "project_id")
	projectID, isUUID := domain.UUID(projectIDStr)
	if !pPresent || !pValid || !isUUID {
		errs["project_id"] = []string{"The project id field is required."}
	}
	estimated, estPresent, estValid := jsonNullableInt(body, "estimated_time")
	if estPresent && !estValid {
		errs["estimated_time"] = []string{"The estimated time must be an integer."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	project, err := db.New(s.pool).GetProjectByID(r.Context(), projectID)
	if err != nil || project.OrganizationID != scope.org.ID {
		validationErr(w, map[string][]string{"project_id": {"The selected project id is invalid."}})
		return
	}
	if scope.has("tasks:create:all") {
		// permitted
	} else if !s.checkScopedTaskPermission(w, r, scope, projectID, "tasks:create") {
		return
	}
	if uniq, err := s.taskNameUnique(r, projectID, name, pgtype.UUID{}); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A task with the same name already exists in the project.")
		return
	}
	task, err := db.New(s.pool).InsertTask(r.Context(), db.InsertTaskParams{
		ID: pgUUID(), Name: name, ProjectID: projectID,
		OrganizationID: scope.org.ID, EstimatedTime: estimated,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventCreated, audit.TypeTask, task.ID, scope.org.ID, pgtype.UUID{}, nil, taskAttrs(task))
	writeJSON(w, http.StatusOK, map[string]any{"data": taskResource(task)})
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	task, ok := s.scopedTask(w, r, scope)
	if !ok {
		return
	}
	body, ok := decodeJSONMap(r)
	if !ok {
		writeMessage(w, http.StatusBadRequest, "invalid json")
		return
	}
	errs := map[string][]string{}
	name, present, valid := jsonStr(body, "name")
	if !present || !valid || len(name) < 1 || len(name) > 255 {
		errs["name"] = []string{"The name field is required."}
	}
	estimated, estPresent, estValid := jsonNullableInt(body, "estimated_time")
	if estPresent && !estValid {
		errs["estimated_time"] = []string{"The estimated time must be an integer."}
	}
	if len(errs) > 0 {
		validationErr(w, errs)
		return
	}
	if scope.has("tasks:update:all") {
		// permitted
	} else if !s.checkScopedTaskPermission(w, r, scope, task.ProjectID, "tasks:update") {
		return
	}
	if uniq, err := s.taskNameUnique(r, task.ProjectID, name, task.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	} else if !uniq {
		uniqueNameErr(w, "name", "A task with the same name already exists in the project.")
		return
	}
	doneAt := task.DoneAt
	if _, has := body["is_done"]; has {
		b, _, bok := jsonBool(body, "is_done")
		if !bok {
			validationErr(w, map[string][]string{"is_done": {"The is done field must be true or false."}})
			return
		}
		if b {
			doneAt = pgtype.Timestamp{Time: time.Now().UTC(), Valid: true}
		} else {
			doneAt = pgtype.Timestamp{}
		}
	}
	if _, has := body["estimated_time"]; has {
		task.EstimatedTime = estimated
	}
	auditBefore := taskAttrs(task)
	task, err := db.New(s.pool).UpdateTask(r.Context(), db.UpdateTaskParams{
		ID: task.ID, Name: name, DoneAt: doneAt, EstimatedTime: task.EstimatedTime,
	})
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventUpdated, audit.TypeTask, task.ID, scope.org.ID, pgtype.UUID{}, auditBefore, taskAttrs(task))
	writeJSON(w, http.StatusOK, map[string]any{"data": taskResource(task)})
}

func (s *Server) handleDestroyTask(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveOrg(w, r, "")
	if !ok {
		return
	}
	task, ok := s.scopedTask(w, r, scope)
	if !ok {
		return
	}
	if scope.has("tasks:delete:all") {
		// permitted
	} else if !s.checkScopedTaskPermission(w, r, scope, task.ProjectID, "tasks:delete") {
		return
	}
	inUse, err := db.New(s.pool).TaskHasTimeEntries(r.Context(), task.ID)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	if inUse {
		writeDomainErr(w, domain.ErrEntityStillInUse("task", "time_entry"))
		return
	}
	if err := db.New(s.pool).DeleteTask(r.Context(), task.ID); err != nil {
		writeMessage(w, http.StatusInternalServerError, "server error")
		return
	}
	s.recordAudit(r, audit.EventDeleted, audit.TypeTask, task.ID, scope.org.ID, pgtype.UUID{}, taskAttrs(task), nil)
	w.WriteHeader(http.StatusNoContent)
}
