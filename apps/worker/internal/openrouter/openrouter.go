// Package openrouter talks to OpenRouter chat and embedding endpoints with
// strict JSON-schema structured outputs, usage/cost accumulation, and
// retryable-vs-fatal error classification.
package openrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/skillsignal/backend/versions"
	"github.com/skillsignal/worker/internal/schemas"
)

// RetryableStatusCodes are transient HTTP failures worth a bounded retry.
var RetryableStatusCodes = map[int]bool{
	408: true, 409: true, 429: true, 500: true, 502: true, 503: true, 504: true, 529: true,
}

// RetryableErrorCodes are transient in-body failure codes.
var RetryableErrorCodes = map[string]bool{
	"rate_limit_exceeded": true, "provider_overloaded": true,
	"provider_unavailable": true, "timeout": true,
}

// Error is a non-retryable provider failure.
type Error struct {
	Message  string
	terminal bool
}

func (e *Error) Error() string { return e.Message }

// RetryableError is a transient failure a bounded job retry may resolve.
type RetryableError struct{ Message string }

func (e *RetryableError) Error() string { return e.Message }

// Client accumulates per-job token usage and cost.
type Client struct {
	fallbacks        map[string][]string
	catalog          map[string]map[string]any
	catalogCheckedAt time.Time
	calls            []Call
	baseURL          string
	timeout          time.Duration
	headers          map[string]string
	http             *http.Client
	promptTokens     int
	completionTokens int
	costUSD          float64
}

// New builds a client for one API key.
func New(apiKey, baseURL string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	baseURL = trimSuffix(baseURL, "/")
	return &Client{
		baseURL: baseURL,
		timeout: timeout,
		headers: map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
			"X-Title":       "skillsignal",
		},
		http: &http.Client{Timeout: timeout},
	}
}

// ResetUsage clears per-job accumulation before dispatching a job.
func (c *Client) ResetUsage() {
	c.promptTokens, c.completionTokens, c.costUSD = 0, 0, 0
	c.calls = nil
}

// Usage returns accumulated prompt tokens, completion tokens, and cost.
func (c *Client) Usage() (int, int, float64) {
	return c.promptTokens, c.completionTokens, c.costUSD
}

func (c *Client) recordUsage(body map[string]any) {
	usage, ok := body["usage"].(map[string]any)
	if !ok {
		return
	}
	if prompt, ok := toFloat(usage["prompt_tokens"]); ok {
		c.promptTokens += int(prompt)
	}
	if completion, ok := toFloat(usage["completion_tokens"]); ok {
		c.completionTokens += int(completion)
	}
	if cost, ok := toFloat(usage["cost"]); ok {
		c.costUSD += cost
	}
}

// CompleteJSON requests a strict-schema JSON object completion.
func (c *Client) CompleteJSON(model, systemPrompt string, userParts any, schemaName string, schema map[string]any, maxOutputTokens int) (map[string]any, error) {
	if _, isObject := userParts.(map[string]any); isObject {
		encoded, err := json.Marshal(userParts)
		if err != nil {
			return nil, &Error{Message: "Model input encoding failed"}
		}
		userParts = string(encoded)
	}
	payload := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": systemPrompt},
			map[string]any{"role": "user", "content": userParts},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": schemaName, "strict": true, "schema": schema,
			},
		},
		"max_tokens": maxOutputTokens,
		"provider":   map[string]any{"require_parameters": true},
	}
	task := "extraction"
	if schemaName == "requirement_assessments" {
		task = "assessment"
	}
	firstCall := len(c.calls)
	body, err := c.withFallback(model, task, false, func(candidate string) (map[string]any, error) {
		payload["model"] = candidate
		body, err := c.post("/chat/completions", payload)
		if err == nil {
			var parsed map[string]any
			parsed, err = ParseJSONCompletion(body)
			if err == nil {
				switch schemaName {
				case "resume_facts":
					_, err = schemas.ValidateResumeExtraction(parsed)
				case "requirement_assessments":
					_, err = schemas.ValidateAssessmentOutput(parsed)
				case "job_requirement_drafts":
					_, _, err = schemas.ValidateRequirementExtraction(parsed)
				}
			}
		}
		return body, err
	})
	for index := firstCall; index < len(c.calls); index++ {
		switch schemaName {
		case "resume_facts":
			c.calls[index].PromptVersion, c.calls[index].SchemaVersion = versions.ExtractionPromptVersion, versions.ResumeFactsSchemaVersion
		case "job_requirement_drafts":
			c.calls[index].PromptVersion, c.calls[index].SchemaVersion = versions.JobRequirementsPromptVersion, versions.JobRequirementsSchemaVersion
		case "requirement_assessments":
			c.calls[index].PromptVersion, c.calls[index].SchemaVersion = versions.AssessmentPromptVersion, versions.RequirementAssessmentSchemaVer
		}
	}
	if err != nil {
		return nil, err
	}
	return ParseJSONCompletion(body)
}

// EmbedTexts embeds inputs positionally.
func (c *Client) EmbedTexts(model string, texts []string) ([][]float64, error) {
	vectors, _, err := c.EmbedTextsWithModel(model, texts)
	return vectors, err
}

// EmbedTextsWithModel reports the model whose coordinate space produced the vectors.
func (c *Client) EmbedTextsWithModel(model string, texts []string) ([][]float64, string, error) {
	return c.embedTexts(model, texts, true)
}

// EmbedTextsExact keeps retrieval queries in the stored vectors' coordinate space.
func (c *Client) EmbedTextsExact(model string, texts []string) ([][]float64, error) {
	vectors, _, err := c.embedTexts(model, texts, false)
	return vectors, err
}

func (c *Client) embedTexts(model string, texts []string, fallback bool) ([][]float64, string, error) {
	inputs := make([]any, 0, len(texts))
	for _, text := range texts {
		inputs = append(inputs, text)
	}
	task := "embedding"
	if !fallback {
		task = "embedding_exact"
	}
	usedModel := model
	body, err := c.withFallback(model, task, true, func(candidate string) (map[string]any, error) {
		usedModel = candidate
		body, err := c.post("/embeddings", map[string]any{"model": candidate, "input": inputs})
		if err == nil {
			_, err = ParseEmbeddingResponse(body, len(inputs))
		}
		return body, err
	})
	if err != nil {
		return nil, "", err
	}
	vectors, err := ParseEmbeddingResponse(body, len(inputs))
	return vectors, usedModel, err
}

func (c *Client) post(path string, payload map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &Error{Message: "Model request encoding failed"}
	}
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, &RetryableError{Message: "Model request transport failed"}
	}
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, &RetryableError{Message: "Model request timed out"}
		}
		return nil, &RetryableError{Message: "Model request transport failed"}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, ClassifyStatus(resp.StatusCode, "Model returned invalid JSON")
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, ClassifyStatus(resp.StatusCode, "Model returned invalid JSON")
	}
	if resp.StatusCode != 200 {
		message := errorString(body["error"], "message")
		if message == "" {
			message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		_ = message
		return nil, ClassifyStatus(resp.StatusCode, "Model provider request failed")
	}
	// OpenRouter can report upstream failures inside HTTP 200.
	if errorObj, ok := body["error"].(map[string]any); ok {
		classified := ClassifyError(errorObj)
		if _, ok := classified.(*RetryableError); ok {
			return nil, &RetryableError{Message: "Model provider temporarily unavailable"}
		}
		return nil, &Error{Message: "Model provider request failed"}
	}
	c.recordUsage(body)
	return body, nil
}

func isTimeout(err error) bool {
	var target interface{ Timeout() bool }
	if errors.As(err, &target) {
		return target.Timeout()
	}
	return false
}

func errorString(value any, key string) string {
	if item, ok := value.(map[string]any); ok {
		if text, ok := item[key].(string); ok {
			return text
		}
	}
	return ""
}

// ClassifyStatus maps HTTP failures to retryable or fatal errors.
func ClassifyStatus(statusCode int, message string) error {
	if RetryableStatusCodes[statusCode] {
		return &RetryableError{Message: message}
	}
	switch statusCode {
	case 402:
		return &Error{Message: "Model credits are exhausted", terminal: true}
	case 401, 403:
		return &Error{Message: "Model credentials were rejected", terminal: true}
	default:
		return &Error{Message: message}
	}
}

// ClassifyError maps in-body 200 failures, keeping file-annotation
// failures fatal so retries do not re-parse and re-charge.
func ClassifyError(errorObj map[string]any) error {
	code, _ := errorObj["code"].(string)
	message := errorString(errorObj, "message")
	if message == "" {
		message = code
	}
	if metadata, ok := errorObj["metadata"].(map[string]any); ok {
		if _, ok := metadata["file_annotations"]; ok {
			return &Error{Message: message}
		}
	}
	if RetryableErrorCodes[code] {
		return &RetryableError{Message: message}
	}
	return &Error{Message: message}
}

// ParseEmbeddingResponse requires exactly one vector per input index.
func ParseEmbeddingResponse(body map[string]any, expectedCount int) ([][]float64, error) {
	data, ok := body["data"].([]any)
	if !ok || len(data) == 0 {
		return nil, &Error{Message: "Embedding response has no data"}
	}
	vectors := make(map[int][]float64)
	for _, item := range data {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, &Error{Message: "Embedding entry is malformed"}
		}
		indexFloat, ok := toFloat(entry["index"])
		index := int(indexFloat)
		if !ok || math.IsNaN(indexFloat) || math.IsInf(indexFloat, 0) || indexFloat != float64(index) || index < 0 || index >= expectedCount {
			return nil, &Error{Message: "Embedding response indexes do not match the request"}
		}
		if _, exists := vectors[index]; exists {
			return nil, &Error{Message: "Embedding response repeats an input index"}
		}
		raw, ok := entry["embedding"].([]any)
		if !ok || len(raw) == 0 {
			return nil, &Error{Message: "Embedding vector is missing"}
		}
		vector := make([]float64, 0, len(raw))
		for _, value := range raw {
			number, ok := toFloat(value)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, &Error{Message: "Embedding vector is not numeric"}
			}
			vector = append(vector, number)
		}
		vectors[index] = vector
	}
	if len(vectors) != expectedCount {
		return nil, &Error{Message: "Embedding response does not cover every input"}
	}
	dimensions := make(map[int]bool)
	for _, vector := range vectors {
		dimensions[len(vector)] = true
	}
	if len(dimensions) != 1 {
		return nil, &Error{Message: "Embedding vectors have inconsistent dimensions"}
	}
	out := make([][]float64, 0, expectedCount)
	for i := 0; i < expectedCount; i++ {
		out = append(out, vectors[i])
	}
	return out, nil
}

// ParseJSONCompletion extracts the first choice object with refusal,
// truncation, and early-stop handling.
func ParseJSONCompletion(body map[string]any) (map[string]any, error) {
	choices, ok := body["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, &Error{Message: "Model response has no choices"}
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, &Error{Message: "Model response choice is malformed"}
	}
	finishReason, _ := choice["finish_reason"].(string)
	if finishReason == "length" {
		return nil, &RetryableError{Message: "Model output was truncated"}
	}
	if finishReason != "" && finishReason != "stop" {
		return nil, &Error{Message: "Model stopped early: " + finishReason}
	}
	content := ""
	refusal := ""
	if message, ok := choice["message"].(map[string]any); ok {
		content, _ = message["content"].(string)
		refusal = errorString(message, "refusal")
	}
	if content == "" {
		if refusal != "" {
			return nil, &Error{Message: "Model refused the extraction request"}
		}
		return nil, &Error{Message: "Model response is empty"}
	}
	var parsed any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, &Error{Message: "Model response is not valid JSON"}
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, &Error{Message: "Model response is not a JSON object"}
	}
	return object, nil
}

func toFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func trimSuffix(text, suffix string) string {
	if len(text) >= len(suffix) && text[len(text)-len(suffix):] == suffix {
		return text[:len(text)-len(suffix)]
	}
	return text
}
