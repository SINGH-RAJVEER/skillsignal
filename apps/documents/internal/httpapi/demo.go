package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/skillsignal/backend/httpx"
)

func (s *Server) demoExtraction(w http.ResponseWriter, r *http.Request) {
	var key, media string
	err := s.pool.QueryRow(r.Context(), `SELECT d.storage_key,d.media_type FROM resume_version v JOIN resume_document d ON d.id=v.resume_document_id WHERE v.id=$1 AND d.organization_id='demo-organization'`, r.PathValue("id")).Scan(&key, &media)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Demo resume version not found")
		return
	}
	if err != nil {
		failure(w, err)
		return
	}
	if media != "text/plain" {
		httpx.WriteError(w, 400, httpx.CodeInvalidDocument, "Demo sources must be plain text")
		return
	}
	body, err := s.store.Get(key)
	if err != nil {
		failure(w, err)
		return
	}
	text := strings.TrimSpace(string(body))
	blocks := map[string]any{"blocks": []map[string]any{{"id": "demo-source", "page": 1, "readingOrder": 0, "text": text, "method": "synthetic-demo"}}, "metadata": map[string]any{"mediaType": media, "pageCount": 1, "blockCount": 1, "characterCount": len(text)}, "warnings": []string{"Synthetic demonstration data"}}
	facts := map[string]any{"skills": []map[string]string{}, "warnings": []string{"Synthetic demonstration data"}}
	if strings.Contains(text, "five years of professional Go experience") {
		facts["skills"] = []map[string]string{{"canonicalName": "Go"}}
	}
	b, _ := json.Marshal(blocks)
	f, _ := json.Marshal(facts)
	if _, err = s.pool.Exec(r.Context(), `UPDATE resume_version SET extraction_blocks=$2::jsonb,normalized_facts=$3::jsonb,quality_state='ready',parser_version='synthetic-demo-1' WHERE id=$1`, r.PathValue("id"), b, f); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]string{"blockId": "demo-source", "quote": text})
}
