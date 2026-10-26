// Package config loads cloud0's typed settings from the environment (and,
// for local development, from ./.env), validates them at startup and offers
// a redacted view safe to log. Secret values are never printed.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// Defaults for every optional variable (spec/architecture.md → Environment variables).
const (
	DefaultPort                = 8087
	DefaultBind                = "127.0.0.1"
	DefaultDBPath              = "./data/cloud0.db"
	DefaultFirstWeek           = "2026-W18"
	DefaultSettleHours         = 48
	DefaultPartitionMarginDays = 1
	DefaultLogLevel            = "info"
	DefaultSmokeWeek           = "2026-W42"
	DefaultE2EPort             = 8097
	DefaultCurrency            = "INR"
)

// TablePattern is the shape a fully-qualified BigQuery table id must have.
var TablePattern = regexp.MustCompile(`^[A-Za-z0-9-]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Config is the validated runtime configuration.
type Config struct {
	// CredentialsPath is GOOGLE_APPLICATION_CREDENTIALS. It is a secret
	// location and is never printed; Redacted() reports only set/unset.
	CredentialsPath string

	Projects           []string
	BillingAccountID   string
	BillingExportTable string
	BigQueryProject    string

	Port   int
	Bind   string
	DBPath string

	FirstWeek           isoweek.Week
	SettleHours         int
	PartitionMarginDays int
	LogLevel            string

	// Currency is the ISO 4217 code of the billing export's money values
	// (CLOUD0_CURRENCY). Display-only: no conversion is ever applied.
	Currency string

	// Test-only knobs, read here so every variable has one home.
	SmokeWeek isoweek.Week
	E2EPort   int
}

// ValidationError names the offending variable so the message can point at .env.example.
type ValidationError struct {
	Variable string
	Reason   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("config: %s %s (see .env.example)", e.Variable, e.Reason)
}

// LoadDotEnv loads ./.env from the working directory when present, never
// overriding variables that are already set. A missing file is not an error.
func LoadDotEnv() error {
	if _, err := os.Stat(".env"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("config: stat .env: %w", err)
	}
	if err := godotenv.Load(".env"); err != nil {
		return fmt.Errorf("config: load .env: %w", err)
	}
	return nil
}

// Load builds a Config from the given lookup (typically os.Getenv) and
// validates it. Parsing and validation errors are *ValidationError values.
func Load(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	var cfg Config
	var errs []error

	cfg.CredentialsPath = get("GOOGLE_APPLICATION_CREDENTIALS")
	cfg.BillingAccountID = get("CLOUD0_BILLING_ACCOUNT_ID")

	cfg.Projects = splitList(get("CLOUD0_GCP_PROJECTS"))
	if len(cfg.Projects) == 0 {
		errs = append(errs, &ValidationError{"CLOUD0_GCP_PROJECTS", "is required: a comma-separated list of billed project IDs"})
	}

	cfg.BillingExportTable = get("CLOUD0_BILLING_EXPORT_TABLE")
	switch {
	case cfg.BillingExportTable == "":
		errs = append(errs, &ValidationError{"CLOUD0_BILLING_EXPORT_TABLE", "is required: the fully-qualified project.dataset.table of the billing export"})
	case !TablePattern.MatchString(cfg.BillingExportTable):
		errs = append(errs, &ValidationError{"CLOUD0_BILLING_EXPORT_TABLE", "must match project.dataset.table (letters, digits, '-' in the project, '_' in dataset and table)"})
	}

	cfg.BigQueryProject = get("CLOUD0_BIGQUERY_PROJECT")
	if cfg.BigQueryProject == "" {
		errs = append(errs, &ValidationError{"CLOUD0_BIGQUERY_PROJECT", "is required: the project that runs BigQuery jobs"})
	}

	port, portVar, err := loadPort(get)
	if err != nil {
		errs = append(errs, &ValidationError{portVar, err.Error()})
	}
	cfg.Port = port

	cfg.Bind = get("CLOUD0_BIND")
	if cfg.Bind == "" {
		cfg.Bind = DefaultBind
	}

	cfg.DBPath = get("CLOUD0_DB_PATH")
	if cfg.DBPath == "" {
		cfg.DBPath = DefaultDBPath
	}

	cfg.FirstWeek, err = loadWeek(get, "CLOUD0_FIRST_WEEK", DefaultFirstWeek)
	if err != nil {
		errs = append(errs, err)
	}

	cfg.SettleHours, err = loadInt(get, "CLOUD0_SETTLE_HOURS", DefaultSettleHours, 0)
	if err != nil {
		errs = append(errs, err)
	}

	cfg.PartitionMarginDays, err = loadInt(get, "CLOUD0_PARTITION_MARGIN_DAYS", DefaultPartitionMarginDays, 0)
	if err != nil {
		errs = append(errs, err)
	}

	cfg.LogLevel = strings.ToLower(get("CLOUD0_LOG_LEVEL"))
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, &ValidationError{"CLOUD0_LOG_LEVEL", fmt.Sprintf("must be one of debug|info|warn|error, got %q", cfg.LogLevel)})
	}

	cfg.Currency = strings.ToUpper(get("CLOUD0_CURRENCY"))
	if cfg.Currency == "" {
		cfg.Currency = DefaultCurrency
	}
	if !currencyPattern.MatchString(cfg.Currency) {
		errs = append(errs, &ValidationError{"CLOUD0_CURRENCY", fmt.Sprintf("must be a 3-letter ISO 4217 code like INR or USD, got %q", get("CLOUD0_CURRENCY"))})
	}

	cfg.SmokeWeek, err = loadWeek(get, "CLOUD0_SMOKE_WEEK", DefaultSmokeWeek)
	if err != nil {
		errs = append(errs, err)
	}

	cfg.E2EPort, err = loadInt(get, "CLOUD0_E2E_PORT", DefaultE2EPort, 1)
	if err != nil {
		errs = append(errs, err)
	} else if cfg.E2EPort > 65535 {
		errs = append(errs, &ValidationError{"CLOUD0_E2E_PORT", "must be a port number 1–65535"})
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// Redacted returns the configuration as key/value pairs suitable for logging.
// The credentials path is reported only as "set" or "unset (ADC)".
func (c Config) Redacted() map[string]any {
	creds := "unset (ADC)"
	if c.CredentialsPath != "" {
		creds = "set"
	}
	return map[string]any{
		"credentials":           creds,
		"projects":              c.Projects,
		"billing_account_set":   c.BillingAccountID != "",
		"billing_export_table":  c.BillingExportTable,
		"bigquery_project":      c.BigQueryProject,
		"port":                  c.Port,
		"bind":                  c.Bind,
		"db_path":               c.DBPath,
		"first_week":            c.FirstWeek.String(),
		"settle_hours":          c.SettleHours,
		"partition_margin_days": c.PartitionMarginDays,
		"log_level":             c.LogLevel,
		"currency":              c.Currency,
	}
}

// Addr is the host:port the HTTP server listens on.
func (c Config) Addr() string { return fmt.Sprintf("%s:%d", c.Bind, c.Port) }

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadPort honours CLOUD0_PORT, then PORT (Cloud Run), then the default.
func loadPort(get func(string) string) (int, string, error) {
	name := "CLOUD0_PORT"
	raw := get(name)
	if raw == "" {
		if p := get("PORT"); p != "" {
			name, raw = "PORT", p
		}
	}
	if raw == "" {
		return DefaultPort, name, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, name, fmt.Errorf("must be a port number 1–65535, got %q", raw)
	}
	return n, name, nil
}

func loadInt(get func(string) string, name string, def, min int) (int, error) {
	raw := get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min {
		return 0, &ValidationError{name, fmt.Sprintf("must be an integer >= %d, got %q", min, raw)}
	}
	return n, nil
}

func loadWeek(get func(string) string, name, def string) (isoweek.Week, error) {
	raw := get(name)
	if raw == "" {
		raw = def
	}
	w, err := isoweek.Parse(raw)
	if err != nil {
		return isoweek.Week{}, &ValidationError{name, fmt.Sprintf("must be an ISO week like 2026-W18, got %q", raw)}
	}
	return w, nil
}
