package httpapi

import (
	"net/http"

	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/domainhttp"
)

func (h *handler) demoSession(w http.ResponseWriter, r *http.Request, s *PostgresStore) {
	var input struct {
		Act string `json:"act"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Act != "employer" && input.Act != "candidate" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Choose the employer or candidate demo"})
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		h.internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	for _, kind := range []string{"employer", "candidate"} {
		_, err = tx.Exec(r.Context(), `INSERT INTO "user"(id,name,email,account_type,email_verified,is_demo) VALUES($1,$2,$3,$4,false,true) ON CONFLICT(id) DO NOTHING`, "demo-"+kind, "Demo "+kind, "demo-"+kind+"@skillsignal.invalid", kind)
		if err != nil {
			h.internalError(w, err)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO organization(id,name) VALUES('demo-organization','SkillSignal demo organization') ON CONFLICT(id) DO NOTHING`)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO organization_member(id,organization_id,user_id,role) VALUES('demo-owner-membership','demo-organization','demo-employer','owner') ON CONFLICT(id) DO NOTHING`)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	client := domainhttp.New(config.Settings{JWTSecret: h.jwtSecret})
	var world map[string]any
	if err := client.Call(r.Context(), "hiring", "/internal/hiring/demo", map[string]string{"organizationId": "demo-organization", "employerUserId": "demo-employer", "candidateUserId": "demo-candidate"}, &world); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if err := client.Call(r.Context(), "evaluation", "/internal/evaluation/demo", world, nil); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	user, err := s.User(r.Context(), "demo-"+input.Act)
	if err != nil {
		h.internalError(w, err)
		return
	}
	if !user.IsDemo || user.AccountType != input.Act {
		h.internalError(w, ErrNotFound)
		return
	}
	h.writeAuth(w, http.StatusOK, user)
}
