// Package config loads service settings from the environment.
// It mirrors the Python service configuration: DATABASE_URL with a DATABASE_HOST/PORT/NAME/USER/PASSWORD fallback, WebURL origin guard, JWT credentials, storage and retention settings, point economics, and worker polling/OpenRouter knobs. Values come only from the environment so container wiring in docker-compose.yml keeps working unchanged.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var durationPattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(ms|s|m|h)$`)

// BillingSettings carries point economics and payment credentials.
type BillingSettings struct {
	PointsPerUSD             int
	MinIndependentPoints     int
	MinEmployerResumePoints  int
	PriceCeilingInputPerMil  float64
	PriceCeilingOutputPerMil float64
	RazorpayKeyID            string
	RazorpayKeySecret        string
	RazorpayWebhookSecret    string
	AdminToken               string
	EnterpriseSalesEmail     string
}

// OpenRouterSettings carries model selection and client limits.
type OpenRouterSettings struct {
	APIKey              string
	BaseURL             string
	ExtractionModel     string
	AssessmentModel     string
	EmbeddingModel      string
	ExtractionFallbacks []string
	AssessmentFallbacks []string
	EmbeddingFallbacks  []string
	TimeoutSeconds      float64
	MaxOutputTokens     int
}

// Settings is the shared API/worker configuration.
type Settings struct {
	DatabaseURL                   string
	WebURL                        string
	JWTSecret                     string
	JWTTTL                        time.Duration
	StorageRoot                   string
	ObjectStorageURL              string
	ObjectStorageToken            string
	IndependentRetentionDays      int
	RetentionSweepIntervalSeconds int
	Billing                       BillingSettings
	WorkerPollIntervalSeconds     float64
	WorkerLeaseSeconds            int
	ParseTimeoutSeconds           float64
	OpenRouter                    OpenRouterSettings
}

// ParseDuration accepts a positive duration like "90s", "500ms", "30m" or "168h".
func ParseDuration(value string) (time.Duration, error) {
	match := durationPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("must be a positive duration like 90s, 30m or 168h")
	}
	amount, err := strconv.ParseFloat(match[1], 64)
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("must be a positive duration like 90s, 30m or 168h")
	}
	var unit time.Duration
	switch match[2] {
	case "ms":
		unit = time.Millisecond
	case "s":
		unit = time.Second
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	}
	return time.Duration(amount * float64(unit)), nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intOr(key, fallback string) (int, error) {
	raw := envOr(key, fallback)
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return value, nil
}

func floatOr(key, fallback string) (float64, error) {
	raw := envOr(key, fallback)
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", key)
	}
	return value, nil
}

// LoadDatabaseURL prefers DATABASE_URL and otherwise builds a postgres URL
// from the DATABASE_HOST/PORT/NAME/USER/PASSWORD parts used by compose.
func LoadDatabaseURL() (string, error) {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url, nil
	}
	names := []string{"DATABASE_HOST", "DATABASE_NAME", "DATABASE_USER", "DATABASE_PASSWORD"}
	values := make(map[string]string, len(names))
	var missing []string
	for _, name := range names {
		values[name] = os.Getenv(name)
		if values[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing database environment variables: %s", strings.Join(missing, ", "))
	}
	port, err := intOr("DATABASE_PORT", "5432")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("postgresql://%s:%s@%s:%d/%s",
		values["DATABASE_USER"], values["DATABASE_PASSWORD"],
		values["DATABASE_HOST"], port, values["DATABASE_NAME"]), nil
}

// Load reads the full service configuration from the environment.
func Load() (Settings, error) {
	var settings Settings
	var err error

	settings.DatabaseURL, err = LoadDatabaseURL()
	if err != nil {
		return Settings{}, err
	}
	settings.WebURL = envOr("WEB_URL", "http://localhost:3000")
	settings.JWTSecret = os.Getenv("JWT_SECRET")
	if len(settings.JWTSecret) < 32 {
		return Settings{}, errors.New("JWT_SECRET environment variable must be at least 32 characters")
	}
	settings.JWTTTL, err = ParseDuration(envOr("JWT_TTL", "168h"))
	if err != nil {
		return Settings{}, fmt.Errorf("JWT_TTL %w", err)
	}
	settings.StorageRoot = envOr("STORAGE_ROOT", ".local-storage")
	settings.ObjectStorageURL = os.Getenv("OBJECT_STORAGE_URL")
	settings.ObjectStorageToken = os.Getenv("OBJECT_STORAGE_TOKEN")
	settings.IndependentRetentionDays, err = intOr("INDEPENDENT_RETENTION_DAYS", "30")
	if err != nil {
		return Settings{}, err
	}
	settings.RetentionSweepIntervalSeconds, err = intOr("RETENTION_SWEEP_INTERVAL_SECONDS", "3600")
	if err != nil {
		return Settings{}, err
	}
	if settings.IndependentRetentionDays <= 0 || settings.RetentionSweepIntervalSeconds < 0 {
		return Settings{}, errors.New("retention settings must be positive")
	}

	billing := BillingSettings{
		RazorpayKeyID:         os.Getenv("RAZORPAY_KEY_ID"),
		RazorpayKeySecret:     os.Getenv("RAZORPAY_KEY_SECRET"),
		RazorpayWebhookSecret: os.Getenv("RAZORPAY_WEBHOOK_SECRET"),
		AdminToken:            os.Getenv("ADMIN_TOKEN"),
		EnterpriseSalesEmail:  envOr("ENTERPRISE_SALES_EMAIL", "sales@skillsignal.app"),
	}
	if billing.PointsPerUSD, err = intOr("POINTS_PER_USD", "1000"); err != nil {
		return Settings{}, err
	}
	if billing.MinIndependentPoints, err = intOr("MIN_POINTS_INDEPENDENT_EVALUATION", "10"); err != nil {
		return Settings{}, err
	}
	if billing.MinEmployerResumePoints, err = intOr("MIN_POINTS_EMPLOYER_RESUME", "5"); err != nil {
		return Settings{}, err
	}
	if billing.PriceCeilingInputPerMil, err = floatOr("PRICE_CEILING_INPUT_USD_PER_MILLION", "3.0"); err != nil {
		return Settings{}, err
	}
	if billing.PriceCeilingOutputPerMil, err = floatOr("PRICE_CEILING_OUTPUT_USD_PER_MILLION", "15.0"); err != nil {
		return Settings{}, err
	}
	settings.Billing = billing

	settings.WorkerPollIntervalSeconds, err = floatOr("WORKER_POLL_INTERVAL_SECONDS", "2")
	if err != nil {
		return Settings{}, err
	}
	leaseSeconds, err := intOr("WORKER_LEASE_SECONDS", "60")
	if err != nil {
		return Settings{}, err
	}
	settings.WorkerLeaseSeconds = leaseSeconds
	settings.ParseTimeoutSeconds, err = floatOr("PARSE_TIMEOUT_SECONDS", "30")
	if err != nil {
		return Settings{}, err
	}
	if settings.WorkerPollIntervalSeconds <= 0 || settings.WorkerLeaseSeconds <= 0 || settings.ParseTimeoutSeconds <= 0 {
		return Settings{}, errors.New("worker intervals and timeouts must be positive")
	}

	openrouter := OpenRouterSettings{
		APIKey:              os.Getenv("OPENROUTER_API_KEY"),
		BaseURL:             envOr("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
		ExtractionModel:     envOr("OPENROUTER_EXTRACTION_MODEL", "nvidia/nemotron-3-super-120b-a12b:free"),
		AssessmentModel:     envOr("OPENROUTER_ASSESSMENT_MODEL", "nvidia/nemotron-3-super-120b-a12b:free"),
		EmbeddingModel:      envOr("OPENROUTER_EMBEDDING_MODEL", "nvidia/nemotron-3-embed-1b:free"),
		ExtractionFallbacks: fallbackModels("OPENROUTER_EXTRACTION_FALLBACKS", "liquid/lfm-2.5-2.6b:free"),
		AssessmentFallbacks: fallbackModels("OPENROUTER_ASSESSMENT_FALLBACKS", "liquid/lfm-2.5-2.6b:free"),
		EmbeddingFallbacks:  fallbackModels("OPENROUTER_EMBEDDING_FALLBACKS", "liquid/lfm-2.5-embedding-350m:free"),
	}
	if openrouter.TimeoutSeconds, err = floatOr("OPENROUTER_TIMEOUT_SECONDS", "90"); err != nil {
		return Settings{}, err
	}
	if openrouter.MaxOutputTokens, err = intOr("OPENROUTER_MAX_OUTPUT_TOKENS", "4096"); err != nil {
		return Settings{}, err
	}
	settings.OpenRouter = openrouter

	return settings, nil
}

// LLMEnabled reports whether model-backed extraction is configured.
func (s Settings) LLMEnabled() bool {
	return s.OpenRouter.APIKey != ""
}

func modelList(value string) []string {
	var models []string
	seen := make(map[string]bool)
	for _, model := range strings.Split(value, ",") {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			models = append(models, model)
			seen[model] = true
		}
	}
	return models
}

func fallbackModels(key, fallback string) []string {
	value, set := os.LookupEnv(key)
	if !set {
		value = fallback
	}
	return modelList(value)
}
