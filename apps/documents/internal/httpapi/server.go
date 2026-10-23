package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/documents"
	"github.com/skillsignal/backend/domainhttp"
	"github.com/skillsignal/backend/httpx"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/backend/retention"
	"github.com/skillsignal/backend/storage"
)

type Server struct {
	pool     *pgxpool.Pool
	store    *storage.Store
	secret   string
	settings config.Settings
}

func Register(mux *http.ServeMux, pool *pgxpool.Pool, settings config.Settings) {
	store, err := storage.New(settings.StorageRoot)
	if err != nil {
		panic(err)
	}
	s := &Server{pool: pool, store: store, secret: settings.JWTSecret, settings: settings}
	mux.HandleFunc("POST /api/admin/retention", s.sweep)
	mux.HandleFunc("GET /internal/documents/expired", s.internal(s.expired))
	mux.HandleFunc("POST /internal/documents/versions/{id}/demo-extraction", s.internal(s.demoExtraction))
	mux.HandleFunc("POST /internal/documents/job-descriptions", s.internal(s.jobDescription))
	mux.HandleFunc("POST /internal/documents", s.internal(s.create))
	mux.HandleFunc("GET /internal/documents/versions/{id}", s.internal(s.version))
	mux.HandleFunc("POST /internal/documents/objects", s.internal(s.putObject))
	mux.HandleFunc("GET /internal/documents/objects/{key...}", s.internal(s.getObject))
	mux.HandleFunc("DELETE /internal/documents/objects/{key...}", s.internal(s.deleteObject))
	mux.HandleFunc("DELETE /internal/documents/{id}", s.internal(s.deleteDocument))
}
func (s *Server) internal(next http.HandlerFunc) http.HandlerFunc {
	return domainhttp.Internal(s.secret, next).ServeHTTP
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, err error) {
	httpx.WriteError(w, 500, httpx.CodeInternalError, "Document operation failed")
}

type upload struct {
	OwnerUserID       string  `json:"ownerUserId"`
	OrganizationID    string  `json:"organizationId"`
	CandidateRecordID *string `json:"candidateRecordId"`
	OriginalName      string  `json:"originalName"`
	MediaType         string  `json:"mediaType"`
	Content           []byte  `json:"content"`
	RetentionDays     int     `json:"retentionDays"`
	IdempotencyKey    string  `json:"idempotencyKey"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var input upload
	if !domainhttp.DecodeJSON(w, r, &input) {
		return
	}
	if (input.OwnerUserID == "") == (input.OrganizationID == "") {
		httpx.WriteError(w, 400, httpx.CodeInvalidRequest, "Supply exactly one document owner")
		return
	}
	if _, err := documents.ValidateDocument(input.Content, input.MediaType, input.OriginalName); err != nil {
		httpx.WriteError(w, 400, httpx.CodeInvalidDocument, err.Error())
		return
	}
	if input.RetentionDays <= 0 {
		input.RetentionDays = 30
		if input.OrganizationID != "" {
			input.RetentionDays = 90
		}
	}
	documentID, versionID := ids.New(), ids.New()
	if input.IdempotencyKey != "" {
		digest := sha256.Sum256([]byte(input.IdempotencyKey))
		suffix := hex.EncodeToString(digest[:])
		documentID = "upload-" + suffix
		versionID = "version-" + suffix
	}
	key := "resumes/" + documentID + documents.SupportedResumeTypes[input.MediaType]
	sum := sha256.Sum256(input.Content)
	checksum := hex.EncodeToString(sum[:])
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if input.IdempotencyKey != "" {
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, input.IdempotencyKey); err != nil {
			failure(w, err)
			return
		}
		var existingOwner, existingOrg, existingChecksum, existingMedia, existingName, existingVersion, existingKey string
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(d.owner_user_id,''),COALESCE(d.organization_id,''),d.checksum,d.media_type,d.original_name,v.id,d.storage_key FROM resume_document d JOIN resume_version v ON v.resume_document_id=d.id WHERE d.id=$1 AND v.version=1 FOR UPDATE OF d,v`, documentID).Scan(&existingOwner, &existingOrg, &existingChecksum, &existingMedia, &existingName, &existingVersion, &existingKey)
		if err == nil {
			if existingOwner != input.OwnerUserID || existingOrg != input.OrganizationID || existingChecksum != checksum || existingMedia != input.MediaType || existingName != input.OriginalName {
				httpx.WriteError(w, 409, httpx.CodeInvalidRequest, "Idempotency key was already used for another document")
				return
			}
			if err = s.store.Put(existingKey, input.Content); err != nil {
				failure(w, err)
				return
			}
			respond(w, 200, map[string]any{"documentId": documentID, "versionId": existingVersion, "storageKey": existingKey})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			failure(w, err)
			return
		}
	}
	if err = s.store.Put(key, input.Content); err != nil {
		failure(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO resume_document(id,owner_user_id,organization_id,candidate_record_id,storage_key,checksum,media_type,size_bytes,original_name,retention_date) VALUES($1,NULLIF($2,''),NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10)`, documentID, input.OwnerUserID, input.OrganizationID, input.CandidateRecordID, key, checksum, input.MediaType, len(input.Content), input.OriginalName, time.Now().UTC().AddDate(0, 0, input.RetentionDays))
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO resume_version(id,organization_id,resume_document_id,version) VALUES($1,NULLIF($2,''),$3,1)`, versionID, input.OrganizationID, documentID)
	}
	if err != nil {
		_ = s.store.Delete(key)
		failure(w, err)
		return
	}
	// A lost commit acknowledgement may still have committed the document. Its deterministic key makes retry safe.
	if err = tx.Commit(r.Context()); err != nil {
		failure(w, err)
		return
	}
	respond(w, 201, map[string]any{"documentId": documentID, "versionId": versionID, "storageKey": key})
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	err := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('id',v.id,'documentId',d.id,'organizationId',d.organization_id,'ownerUserId',d.owner_user_id,'storageKey',d.storage_key,'originalName',d.original_name,'mediaType',d.media_type,'qualityState',v.quality_state,'facts',COALESCE(v.normalized_facts,'{}'::jsonb),'blocks',COALESCE(v.extraction_blocks,'{}'::jsonb)) FROM resume_version v JOIN resume_document d ON d.id=v.resume_document_id WHERE v.id=$1`, r.PathValue("id")).Scan(&raw)
	if err != nil {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Resume version not found")
		return
	}
	respond(w, 200, json.RawMessage(raw))
}
func (s *Server) putObject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Key     string `json:"key"`
		Content []byte `json:"content"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Key == "" {
		httpx.WriteError(w, 400, httpx.CodeInvalidRequest, "Invalid object")
		return
	}
	if err := s.store.Put(input.Key, input.Content); err != nil {
		failure(w, err)
		return
	}
	respond(w, 201, map[string]string{"key": input.Key})
}
func (s *Server) getObject(w http.ResponseWriter, r *http.Request) {
	content, err := s.store.Get(r.PathValue("key"))
	if err != nil {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Document not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(content)
}
func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("key")); err != nil {
		failure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var key string
	err = tx.QueryRow(r.Context(), `DELETE FROM resume_document WHERE id=$1 RETURNING storage_key`, r.PathValue("id")).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(204)
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	if err = s.store.Delete(key); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		failure(w, err)
		return
	}
	w.WriteHeader(204)
}

// Upload reads a multipart resume without accepting the browser's filename as a storage key.
func Upload(r *http.Request, field string) ([]byte, string, string, error) {
	file, header, err := r.FormFile(field)
	if err != nil {
		return nil, "", "", err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, documents.MaxResumeBytes+1))
	mediaType, _, _ := mime.ParseMediaType(header.Header.Get("Content-Type"))
	return content, header.Filename, mediaType, err
}

// Source uploads use generated object keys and do not create resume records.
func (s *Server) jobDescription(w http.ResponseWriter, r *http.Request) {
	var input struct {
		OriginalName string `json:"originalName"`
		MediaType    string `json:"mediaType"`
		Content      []byte `json:"content"`
	}
	if !domainhttp.DecodeJSON(w, r, &input) {
		return
	}
	if len(input.Content) > 10<<20 {
		httpx.WriteError(w, 400, httpx.CodeInvalidDocument, "Job description exceeds 10 MB")
		return
	}
	validated, err := documents.ValidateJobDescription(input.Content, input.MediaType, input.OriginalName)
	if err != nil {
		httpx.WriteError(w, 400, httpx.CodeInvalidDocument, err.Error())
		return
	}
	key := "job-descriptions/" + ids.New() + validated.Extension
	if err := s.store.Put(key, input.Content); err != nil {
		failure(w, err)
		return
	}
	respond(w, 201, map[string]string{"storageKey": key, "mediaType": validated.MediaType})
}
func (s *Server) expired(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	if raw := r.URL.Query().Get("now"); raw != "" {
		var err error
		now, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.WriteError(w, 400, httpx.CodeInvalidRequest, "Invalid retention time")
			return
		}
	}
	rows, err := s.pool.Query(r.Context(), `SELECT d.id,COALESCE(array_agg(v.id) FILTER(WHERE v.id IS NOT NULL),'{}') FROM resume_document d LEFT JOIN resume_version v ON v.resume_document_id=d.id WHERE d.retention_date <= $1 GROUP BY d.id ORDER BY d.retention_date,d.id LIMIT 500`, now)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type expiredDocument struct {
		ID         string   `json:"id"`
		VersionIDs []string `json:"versionIds"`
	}
	items := []expiredDocument{}
	for rows.Next() {
		var item expiredDocument
		if err = rows.Scan(&item.ID, &item.VersionIDs); err != nil {
			failure(w, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, items)
}

func (s *Server) sweep(w http.ResponseWriter, r *http.Request) {
	provided := r.Header.Get("X-Admin-Token")
	if provided == "" {
		provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if s.settings.Billing.AdminToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.settings.Billing.AdminToken)) != 1 {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Route not found")
		return
	}
	result, err := retention.Sweep(r.Context(), s.settings)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	respond(w, 200, result)
}
