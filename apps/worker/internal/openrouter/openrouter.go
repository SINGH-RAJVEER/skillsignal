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
	"net/http"
	"time"
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
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// RetryableError is a transient failure a bounded job retry may resolve.
type RetryableError struct{ Message string }

func (e *RetryableError) Error() string { return e.Message }

// Client accumulates per-job token usage and cost.
type Client struct {
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
		"provider":   map[string]any{"require_parameters": true, "data_collection": "deny"},
	}
	body, err := c.post("/chat/completions", payload)
	if err != nil {
		return nil, err
	}
	return ParseJSONCompletion(body)
}

// EmbedTexts embeds inputs positionally.
func (c *Client) EmbedTexts(model string, texts []string) ([][]float64, error) {
	inputs := make([]any, 0, len(texts))
	for _, text := range texts {
		inputs = append(inputs, text)
	}
	body, err := c.post("/embeddings", map[string]any{"model": model, "input": inputs})
	if err != nil {
		return nil, err
	}
	return ParseEmbeddingResponse(body, len(inputs))
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
		return nil, ClassifyStatus(resp.StatusCode, message)
	}
	// OpenRouter can report upstream failures inside HTTP 200.
	if errorObj, ok := body["error"].(map[string]any); ok {
		return nil, ClassifyError(errorObj)
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
		return &Error{Message: "Model credits are exhausted"}
	case 401, 403:
		return &Error{Message: "Model credentials were rejected"}
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
		if !ok || index < 0 || index >= expectedCount {
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
			if !ok {
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
