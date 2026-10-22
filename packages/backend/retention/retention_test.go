package retention

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/skillsignal/backend/config"
)

func TestSweepResumesInDependencyOrder(t *testing.T) {
	for _, failEvaluation := range []bool{false, true} {
		t.Run(map[bool]string{true: "dependency failure", false: "complete"}[failEvaluation], func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Internal-Secret") != "secret" {
					t.Error("missing service authentication")
				}
				calls = append(calls, r.URL.Path)
				switch r.URL.Path {
				case "/internal/documents/expired":
					_, _ = w.Write([]byte(`[{"id":"document","versionIds":["version"]}]`))
				case "/internal/evaluation/retention":
					_, _ = w.Write([]byte(`{"purged":2}`))
				case "/internal/evaluation/purge-documents":
					if failEvaluation {
						w.WriteHeader(503)
					}
				}
			}))
			defer server.Close()
			for _, key := range []string{"BILLING_URL", "DOCUMENTS_URL", "HIRING_URL", "EVALUATION_URL"} {
				t.Setenv(key, server.URL)
			}
			result, err := Sweep(context.Background(), config.Settings{JWTSecret: "secret"})
			want := []string{"/internal/billing/retention", "/internal/evaluation/retention", "/internal/documents/expired", "/internal/evaluation/purge-documents"}
			if !failEvaluation {
				want = append(want, "/internal/hiring/purge-documents", "/internal/documents/document")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls %v, want %v", calls, want)
			}
			if (err != nil) != failEvaluation {
				t.Fatalf("unexpected error %v", err)
			}
			if !failEvaluation && (result.DocumentsPurged != 1 || result.IndependentEvaluationsPurged != 2) {
				t.Fatalf("counts %+v", result)
			}
		})
	}
}
