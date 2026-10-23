package openrouter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestFallbackOrderCatalogRefreshAndEmbeddingValidation(t *testing.T) {
	var attempted []string
	catalogCalls := 0
	paid := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			catalogCalls++
			price := "0"
			if paid {
				price = "0.01"
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"id": "primary", "pricing": map[string]string{"prompt": price, "completion": "0"}, "architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"embeddings"}}},
				map[string]any{"id": "fallback", "pricing": map[string]string{"prompt": "0", "completion": "0"}, "architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"embeddings"}}},
			}})
			return
		}
		var payload struct{ Model string }
		json.NewDecoder(r.Body).Decode(&payload)
		attempted = append(attempted, payload.Model)
		if payload.Model == "primary" {
			// A malformed successful response must advance to the fallback.
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0.5, "embedding": []int{1, 0}}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "fallback", "data": []any{map[string]any{"index": 0, "embedding": []int{0, 1}}}})
	}))
	defer server.Close()
	client := New("test", server.URL, time.Second)
	client.ConfigureFallbacks("embedding", "primary", []string{"fallback"})
	vectors, model, err := client.EmbedTextsWithModel("primary", []string{"documented skill"})
	if err != nil || model != "fallback" || !reflect.DeepEqual(vectors, [][]float64{{0, 1}}) || !reflect.DeepEqual(attempted, []string{"primary", "fallback"}) {
		t.Fatalf("vectors=%v model=%s attempts=%v err=%v", vectors, model, attempted, err)
	}
	paid = true
	client.catalogCheckedAt = time.Now().Add(-catalogRefreshInterval)
	attempted = nil
	_, _, err = client.EmbedTextsWithModel("primary", []string{"documented skill"})
	if err != nil || catalogCalls != 2 || !reflect.DeepEqual(attempted, []string{"fallback"}) {
		t.Fatalf("paid model was not skipped: attempts=%v catalogs=%d err=%v", attempted, catalogCalls, err)
	}
	if _, err := client.EmbedTextsExact("primary", []string{"query"}); err == nil {
		t.Fatal("exact retrieval silently switched model spaces")
	}
}

func TestEmbeddingIndexesMustBeIntegers(t *testing.T) {
	_, err := ParseEmbeddingResponse(map[string]any{"data": []any{map[string]any{"index": 0.5, "embedding": []any{1.0, 0.0}}}}, 1)
	if err == nil {
		t.Fatal("fractional index was accepted")
	}
}

func TestAuthenticationFailureIsSafeAndDoesNotTryFallback(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			var data []any
			for _, id := range []string{"primary", "fallback"} {
				data = append(data, map[string]any{"id": id, "pricing": map[string]string{"prompt": "0", "completion": "0"}, "architecture": map[string]any{"input_modalities": []string{"text"}}, "supported_parameters": []string{"structured_outputs"}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "sensitive provider diagnostic"}})
	}))
	defer server.Close()
	client := New("fixture", server.URL, time.Second)
	client.ConfigureFallbacks("extraction", "primary", []string{"fallback"})
	_, err := client.CompleteJSON("primary", "test", "synthetic input", "resume_facts", map[string]any{}, 100)
	if err == nil || attempts != 1 || len(client.Calls()) != 1 || client.Calls()[0].FallbackReason != "Model credentials were rejected" {
		t.Fatalf("attempts=%d calls=%+v err=%v", attempts, client.Calls(), err)
	}
	if client.Calls()[0].PromptVersion != "3" || client.Calls()[0].SchemaVersion != "3" {
		t.Fatalf("failed extraction omitted versions: %+v", client.Calls())
	}
}
