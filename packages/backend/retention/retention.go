// Package retention coordinates deletion through each domain's owning API.
package retention

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/domainhttp"
)

type Result struct {
	DocumentsPurged              int `json:"documentsPurged"`
	IndependentEvaluationsPurged int `json:"independentEvaluationsPurged"`
}

type expiredDocument struct {
	ID         string   `json:"id"`
	VersionIDs []string `json:"versionIds"`
}

// Start runs one retryable sweep at a time until the process shuts down.
func Start(ctx context.Context, settings config.Settings) {
	if settings.RetentionSweepIntervalSeconds <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				result, err := Sweep(passCtx, settings)
				cancel()
				if err != nil {
					log.Printf("retention sweep failed: %T", err)
				} else if result.DocumentsPurged+result.IndependentEvaluationsPurged > 0 {
					log.Printf("retention removed %d documents and %d independent evaluations", result.DocumentsPurged, result.IndependentEvaluationsPurged)
				}
				interval := time.Duration(settings.RetentionSweepIntervalSeconds) * time.Second
				if err != nil && interval > time.Minute {
					interval = time.Minute
				}
				timer.Reset(interval)
			}
		}
	}()
}

// Sweep removes dependent records before their source documents. Every domain
// operation is idempotent so a partial sweep resumes on its next invocation.
func Sweep(ctx context.Context, settings config.Settings) (Result, error) {
	client := domainhttp.New(settings)
	var result Result
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := client.Call(ctx, "billing", "/internal/billing/retention", map[string]any{}, nil); err != nil {
		return result, fmt.Errorf("billing retention: %w", err)
	}
	var independent struct {
		Purged int `json:"purged"`
	}
	if err := client.Call(ctx, "evaluation", "/internal/evaluation/retention", map[string]any{"now": now}, &independent); err != nil {
		return result, fmt.Errorf("independent retention: %w", err)
	}
	result.IndependentEvaluationsPurged = independent.Purged
	var documents []expiredDocument
	if err := client.Call(ctx, "documents", "/internal/documents/expired?now="+url.QueryEscape(now), nil, &documents); err != nil {
		return result, err
	}
	for _, document := range documents {
		payload := map[string]any{"documentIds": []string{document.ID}, "versionIds": document.VersionIDs, "now": now}
		if err := client.Call(ctx, "hiring", "/internal/hiring/prepare-purge-documents", payload, nil); err != nil {
			return result, err
		}
		if err := client.Call(ctx, "evaluation", "/internal/evaluation/purge-documents", payload, nil); err != nil {
			return result, err
		}
		if err := client.Call(ctx, "hiring", "/internal/hiring/purge-documents", payload, nil); err != nil {
			return result, err
		}
		if err := client.Request(ctx, http.MethodDelete, "documents", "/internal/documents/"+url.PathEscape(document.ID), nil, nil); err != nil {
			return result, err
		}
		result.DocumentsPurged++
	}
	return result, nil
}
