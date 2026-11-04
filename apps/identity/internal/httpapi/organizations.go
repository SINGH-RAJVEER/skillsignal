package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/skillsignal/backend/httpx"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/backend/joinpolicy"
)

func (h *handler) registerOrganizations(mux *http.ServeMux, s *PostgresStore) {
	mux.HandleFunc("POST /api/demo/session", func(w http.ResponseWriter, r *http.Request) { h.demoSession(w, r, s) })
	mux.HandleFunc("GET /internal/identity/organizations/{id}/retention", func(w http.ResponseWriter, r *http.Request) {
		if h.jwtSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Secret")), []byte(h.jwtSecret)) != 1 {
			httpx.WriteError(w, 404, "NOT_FOUND", "Route not found")
			return
		}
		var days int
		if err := s.pool.QueryRow(r.Context(), `SELECT retention_days FROM organization WHERE id=$1`, r.PathValue("id")).Scan(&days); err != nil {
			h.organizationError(w, err)
			return
		}
		writeJSON(w, 200, map[string]int{"retentionDays": days})
	})
	mux.HandleFunc("GET /internal/identity/users/{id}/access", func(w http.ResponseWriter, r *http.Request) {
		if h.jwtSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Secret")), []byte(h.jwtSecret)) != 1 {
			httpx.WriteError(w, 401, "UNAUTHORIZED", "Authentication required")
			return
		}
		u, err := s.User(r.Context(), r.PathValue("id"))
		if err != nil {
			h.organizationError(w, err)
			return
		}
		rows, err := s.pool.Query(r.Context(), `SELECT m.organization_id,m.role,o.retention_days FROM organization_member m JOIN organization o ON o.id=m.organization_id WHERE m.user_id=$1`, u.ID)
		if err != nil {
			h.internalError(w, err)
			return
		}
		defer rows.Close()
		members := []map[string]any{}
		for rows.Next() {
			var org, role string
			var days int
			if err := rows.Scan(&org, &role, &days); err != nil {
				h.internalError(w, err)
				return
			}
			members = append(members, map[string]any{"organizationId": org, "role": role, "retentionDays": days})
		}
		if rows.Err() != nil {
			h.internalError(w, rows.Err())
			return
		}
		writeJSON(w, 200, map[string]any{"userId": u.ID, "accountType": u.AccountType, "demoReadOnly": u.IsDemo, "memberships": members})
	})
	mux.HandleFunc("/api/organizations", func(w http.ResponseWriter, r *http.Request) { h.organizations(w, r, s) })
	mux.HandleFunc("/api/organizations/{organization}/members", func(w http.ResponseWriter, r *http.Request) { h.members(w, r, s) })
	mux.HandleFunc("DELETE /api/organizations/{organization}/members/{user}", func(w http.ResponseWriter, r *http.Request) { h.members(w, r, s) })
	mux.HandleFunc("/api/organizations/{organization}/join-policy", func(w http.ResponseWriter, r *http.Request) { h.policy(w, r, s) })
	mux.HandleFunc("/api/organizations/{organization}/join-policy/{rules}", func(w http.ResponseWriter, r *http.Request) { h.policy(w, r, s) })
	mux.HandleFunc("DELETE /api/organizations/{organization}/join-policy/{rules}/{value}", func(w http.ResponseWriter, r *http.Request) { h.policy(w, r, s) })
}

func (h *handler) organizationError(w http.ResponseWriter, err error) {
	var pg *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, 404, "NOT_FOUND", "Organization or user not found")
		return
	}
	if errors.As(err, &pg) && pg.Code == "23505" {
		httpx.WriteError(w, 409, "ALREADY_MEMBER", "Membership or rule already exists")
		return
	}
	h.internalError(w, err)
}
func (h *handler) employer(w http.ResponseWriter, r *http.Request) (User, bool) {
	u, ok := h.requireUser(w, r, r.Method != "GET")
	if !ok {
		return u, false
	}
	if u.AccountType != "employer" {
		httpx.WriteError(w, 403, "FORBIDDEN", "Employer account required")
		return u, false
	}
	return u, true
}
func (h *handler) organizationRole(w http.ResponseWriter, r *http.Request, s *PostgresStore, u User, owner bool) bool {
	var role string
	err := s.pool.QueryRow(r.Context(), `SELECT role FROM organization_member WHERE organization_id=$1 AND user_id=$2`, r.PathValue("organization"), u.ID).Scan(&role)
	if err != nil {
		h.organizationError(w, err)
		return false
	}
	if owner && role != "owner" {
		httpx.WriteError(w, 404, "NOT_FOUND", "Organization not found")
		return false
	}
	return true
}
func (h *handler) organizations(w http.ResponseWriter, r *http.Request, s *PostgresStore) {
	u, ok := h.employer(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "GET":
		rows, err := s.pool.Query(r.Context(), `SELECT o.id,o.name,m.role FROM organization o JOIN organization_member m ON m.organization_id=o.id WHERE m.user_id=$1 ORDER BY o.created_at`, u.ID)
		if err != nil {
			h.internalError(w, err)
			return
		}
		defer rows.Close()
		result := []map[string]string{}
		for rows.Next() {
			var id, name, role string
			if err := rows.Scan(&id, &name, &role); err != nil {
				h.internalError(w, err)
				return
			}
			result = append(result, map[string]string{"id": id, "name": name, "role": role})
		}
		if rows.Err() != nil {
			h.internalError(w, rows.Err())
			return
		}
		writeJSON(w, 200, result)
	case "POST":
		var input struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || len(input.Name) > 200 {
			httpx.WriteError(w, 400, "INVALID_REQUEST", "Organization name is required and must be at most 200 characters")
			return
		}
		tx, err := s.pool.Begin(r.Context())
		if err != nil {
			h.internalError(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		id := ids.New()
		_, err = tx.Exec(r.Context(), `INSERT INTO organization(id,name) VALUES($1,$2)`, id, input.Name)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO organization_member(id,organization_id,user_id,role) VALUES($1,$2,$3,'owner')`, ids.New(), id, u.ID)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			h.organizationError(w, err)
			return
		}
		writeJSON(w, 201, map[string]string{"id": id, "name": input.Name, "role": "owner"})
	default:
		w.WriteHeader(405)
	}
}
func (h *handler) members(w http.ResponseWriter, r *http.Request, s *PostgresStore) {
	u, ok := h.employer(w, r)
	if !ok || !h.organizationRole(w, r, s, u, r.Method != "GET") {
		return
	}
	org := r.PathValue("organization")
	switch r.Method {
	case "GET":
		rows, err := s.pool.Query(r.Context(), `SELECT u.id,u.name,u.email,m.role FROM organization_member m JOIN "user" u ON u.id=m.user_id WHERE m.organization_id=$1 ORDER BY u.name,u.email`, org)
		if err != nil {
			h.internalError(w, err)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, name, email, role string
			if err := rows.Scan(&id, &name, &email, &role); err != nil {
				h.internalError(w, err)
				return
			}
			out = append(out, map[string]string{"userId": id, "name": name, "email": email, "role": role})
		}
		if rows.Err() != nil {
			h.internalError(w, rows.Err())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var input struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Role != "viewer" && input.Role != "recruiter" {
			httpx.WriteError(w, 400, "INVALID_REQUEST", "Member role must be recruiter or viewer")
			return
		}
		var id string
		err := s.pool.QueryRow(r.Context(), `SELECT id FROM "user" WHERE email=$1 AND account_type='employer'`, strings.ToLower(strings.TrimSpace(input.Email))).Scan(&id)
		if err == nil {
			_, err = s.pool.Exec(r.Context(), `INSERT INTO organization_member(id,organization_id,user_id,role) VALUES($1,$2,$3,$4)`, ids.New(), org, id, input.Role)
		}
		if err != nil {
			h.organizationError(w, err)
			return
		}
		writeJSON(w, 201, map[string]string{"userId": id, "role": input.Role})
	case "DELETE":
		var role string
		err := s.pool.QueryRow(r.Context(), `SELECT role FROM organization_member WHERE organization_id=$1 AND user_id=$2`, org, r.PathValue("user")).Scan(&role)
		if err != nil {
			h.organizationError(w, err)
			return
		}
		if role == "owner" {
			httpx.WriteError(w, 409, "OWNER_REQUIRED", "Organization owner cannot be removed")
			return
		}
		_, err = s.pool.Exec(r.Context(), `DELETE FROM organization_member WHERE organization_id=$1 AND user_id=$2 AND role<>'owner'`, org, r.PathValue("user"))
		if err != nil {
			h.internalError(w, err)
			return
		}
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}
func (h *handler) policy(w http.ResponseWriter, r *http.Request, s *PostgresStore) {
	u, ok := h.employer(w, r)
	if !ok || !h.organizationRole(w, r, s, u, true) {
		return
	}
	org := r.PathValue("organization")
	rule := r.PathValue("rules")
	if rule == "" {
		if r.Method == "GET" {
			var role string
			var domains, emails []byte
			err := s.pool.QueryRow(r.Context(), `SELECT default_member_role,COALESCE((SELECT json_agg(domain ORDER BY domain) FROM organization_email_domain WHERE organization_id=$1),'[]'),COALESCE((SELECT json_agg(email ORDER BY email) FROM organization_allowed_email WHERE organization_id=$1),'[]') FROM organization WHERE id=$1`, org).Scan(&role, &domains, &emails)
			if err != nil {
				h.organizationError(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"defaultRole": role, "domains": json.RawMessage(domains), "emails": json.RawMessage(emails)})
			return
		}
		if r.Method == "PUT" {
			var input struct {
				DefaultRole      string `json:"defaultRole"`
				DefaultRoleSnake string `json:"default_role"`
			}
			if !decodeJSON(w, r, &input) {
				return
			}
			if input.DefaultRole == "" {
				input.DefaultRole = input.DefaultRoleSnake
			}
			if input.DefaultRole != "viewer" && input.DefaultRole != "recruiter" {
				httpx.WriteError(w, 400, "INVALID_REQUEST", "Default role must be recruiter or viewer")
				return
			}
			_, err := s.pool.Exec(r.Context(), `UPDATE organization SET default_member_role=$1,updated_at=now() WHERE id=$2`, input.DefaultRole, org)
			if err != nil {
				h.internalError(w, err)
				return
			}
			writeJSON(w, 200, map[string]string{"defaultRole": input.DefaultRole})
			return
		}
		w.WriteHeader(405)
		return
	}
	table, column := "", ""
	switch rule {
	case "domains":
		table, column = "organization_email_domain", "domain"
	case "emails":
		table, column = "organization_allowed_email", "email"
	default:
		httpx.WriteError(w, 404, "NOT_FOUND", "Route not found")
		return
	}
	value := r.PathValue("value")
	if r.Method == "POST" {
		var input map[string]string
		if !decodeJSON(w, r, &input) {
			return
		}
		value = input[column]
	} else if r.Method != "DELETE" {
		w.WriteHeader(405)
		return
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if rule == "domains" {
		normalized, err := joinpolicy.NormalizeDomain(value)
		if err != nil {
			httpx.WriteError(w, 400, "INVALID_REQUEST", err.Error())
			return
		}
		value = normalized
	} else {
		address, err := mail.ParseAddress(value)
		if err != nil || address.Address != value {
			httpx.WriteError(w, 400, "INVALID_REQUEST", "Enter a valid email address")
			return
		}
	}
	if r.Method == "POST" {
		_, err := s.pool.Exec(r.Context(), `INSERT INTO `+table+` (id,organization_id,`+column+`) VALUES($1,$2,$3)`, ids.New(), org, value)
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				httpx.WriteError(w, 409, "RULE_EXISTS", "Join policy rule already exists")
				return
			}
			h.internalError(w, err)
			return
		}
		writeJSON(w, 201, map[string]string{column: value})
		return
	}
	result, err := s.pool.Exec(r.Context(), `DELETE FROM `+table+` WHERE organization_id=$1 AND `+column+`=$2`, org, value)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		httpx.WriteError(w, 404, "NOT_FOUND", "Join policy rule not found")
		return
	}
	w.WriteHeader(204)
}
