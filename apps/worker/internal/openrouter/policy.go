package openrouter

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

type Call struct {
	Model, Provider, RequestID, Task, Status, FallbackReason string
	PromptVersion, SchemaVersion                             string
	PromptTokens, CompletionTokens                           int
	CostUSD                                                  float64
	LatencyMS                                                int64
}

func (c *Client) ConfigureFallbacks(task, primary string, fallbacks []string) {
	if c.fallbacks == nil {
		c.fallbacks = make(map[string][]string)
	}
	c.fallbacks[task] = append([]string{primary}, fallbacks...)
}

func (c *Client) Calls() []Call { return append([]Call(nil), c.calls...) }

const catalogRefreshInterval = 5 * time.Minute

// ValidateConfigured checks every configured candidate before queue work starts.
// Invalid candidates remain unavailable; each task must have a usable candidate.
func (c *Client) ValidateConfigured() error {
	for _, task := range []string{"extraction", "assessment", "embedding"} {
		var available bool
		for _, model := range c.fallbacks[task] {
			if err := c.checkModel(model, task == "embedding"); err == nil {
				available = true
			}
		}
		if !available {
			return &Error{Message: "No free capable model is available for " + task}
		}
	}
	return nil
}

func (c *Client) checkModel(model string, embedding bool) error {
	if c.catalog == nil || time.Since(c.catalogCheckedAt) >= catalogRefreshInterval {
		req, err := http.NewRequest("GET", c.baseURL+"/models?output_modalities=all", nil)
		if err != nil {
			return &Error{Message: "Model catalog request failed"}
		}
		for key, value := range c.headers {
			req.Header.Set(key, value)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return &RetryableError{Message: "Model catalog unavailable"}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return ClassifyStatus(resp.StatusCode, "Model catalog unavailable")
		}
		var body struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
			return &Error{Message: "Model catalog is invalid"}
		}
		c.catalog = make(map[string]map[string]any)
		for _, entry := range body.Data {
			if id, ok := entry["id"].(string); ok {
				c.catalog[id] = entry
			}
		}
		c.catalogCheckedAt = time.Now()
	}
	entry := c.catalog[model]
	if entry == nil {
		return &Error{Message: "Configured model is absent from the catalog"}
	}
	pricing, _ := entry["pricing"].(map[string]any)
	for _, field := range []string{"prompt", "completion", "request", "image", "web_search", "internal_reasoning", "input_cache_read", "input_cache_write"} {
		value, ok := pricing[field]
		if !ok && (field != "prompt" && field != "completion" || embedding && field == "completion") {
			continue
		}
		number, err := strconv.ParseFloat(fmt.Sprint(value), 64)
		if err != nil || math.IsNaN(number) || number != 0 {
			return &Error{Message: "Configured model must have zero token pricing"}
		}
	}
	architecture, _ := entry["architecture"].(map[string]any)
	inputs, _ := architecture["input_modalities"].([]any)
	if !containsParameter(inputs, "text") {
		return &Error{Message: "Configured model does not accept text"}
	}
	if embedding {
		outputs, _ := architecture["output_modalities"].([]any)
		if !containsParameter(outputs, "embeddings") {
			return &Error{Message: "Configured model does not support embeddings"}
		}
	} else {
		parameters, _ := entry["supported_parameters"].([]any)
		for _, parameter := range parameters {
			if parameter == "structured_outputs" {
				return nil
			}
		}
		return &Error{Message: "Configured model does not support structured outputs"}
	}
	return nil
}

func containsParameter(values []any, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (c *Client) withFallback(model, task string, embedding bool, run func(string) (map[string]any, error)) (map[string]any, error) {
	models := c.fallbacks[task]
	if len(models) == 0 {
		models = []string{model}
	}
	var last error
	for _, candidate := range models {
		started := time.Now()
		call := Call{Model: candidate, Task: task, Status: "failed"}
		if last != nil {
			call.FallbackReason = "Previous model attempt failed"
		}
		if err := c.checkModel(candidate, embedding); err != nil {
			last = err
			call.FallbackReason = "Catalog capability or free-price check failed"
			call.LatencyMS = time.Since(started).Milliseconds()
			c.calls = append(c.calls, call)
			continue
		}
		body, err := run(candidate)
		call.LatencyMS = time.Since(started).Milliseconds()
		if body != nil {
			if actual, ok := body["model"].(string); ok {
				call.Model = actual
			}
			call.Provider, _ = body["provider"].(string)
			call.RequestID, _ = body["id"].(string)
			usage, _ := body["usage"].(map[string]any)
			prompt, _ := toFloat(usage["prompt_tokens"])
			completion, _ := toFloat(usage["completion_tokens"])
			call.CostUSD, _ = toFloat(usage["cost"])
			call.PromptTokens, call.CompletionTokens = int(prompt), int(completion)
		}
		if err == nil {
			call.Status = "succeeded"
		} else {
			call.FallbackReason = failureCategory(err)
		}
		c.calls = append(c.calls, call)
		if err == nil {
			return body, nil
		}
		last = err
		if fatal, ok := err.(*Error); ok && fatal.terminal {
			return nil, err
		}
	}
	return nil, last
}

func failureCategory(err error) string {
	if typed, ok := err.(*Error); ok && typed.terminal {
		return typed.Message
	}
	if _, ok := err.(*RetryableError); ok {
		return "Transient provider or transport failure"
	}
	return "Invalid model output or provider request failure"
}
