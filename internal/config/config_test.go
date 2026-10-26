package config

import (
	"errors"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func minimal() map[string]string {
	return map[string]string{
		"CLOUD0_GCP_PROJECTS":         "project-a, project-b",
		"CLOUD0_BILLING_EXPORT_TABLE": "my-project.my_dataset.my_table",
		"CLOUD0_BIGQUERY_PROJECT":     "my-project",
	}
}

func TestLoad_AppliesDefaultsForOptionalVariables(t *testing.T) {
	cfg, err := Load(env(minimal()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strings.Join(cfg.Projects, "|"); got != "project-a|project-b" {
		t.Errorf("Projects = %q", got)
	}
	if cfg.Port != 8087 || cfg.Bind != "127.0.0.1" || cfg.Addr() != "127.0.0.1:8087" {
		t.Errorf("port/bind defaults wrong: %d %q", cfg.Port, cfg.Bind)
	}
	if cfg.DBPath != "./data/cloud0.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.FirstWeek.String() != "2026-W18" || cfg.SmokeWeek.String() != "2026-W42" {
		t.Errorf("weeks = %s %s", cfg.FirstWeek, cfg.SmokeWeek)
	}
	if cfg.SettleHours != 48 || cfg.PartitionMarginDays != 1 || cfg.LogLevel != "info" || cfg.E2EPort != 8097 || cfg.Currency != "INR" {
		t.Errorf("optional defaults wrong: %+v", cfg)
	}
	if cfg.CredentialsPath != "" || cfg.BillingAccountID != "" {
		t.Errorf("optional strings should be empty")
	}
}

func TestLoad_ReadsEveryVariable(t *testing.T) {
	m := minimal()
	m["GOOGLE_APPLICATION_CREDENTIALS"] = "/outside/repo/key.json"
	m["CLOUD0_BILLING_ACCOUNT_ID"] = "XXXXXX-XXXXXX-XXXXXX"
	m["CLOUD0_PORT"] = "9000"
	m["CLOUD0_BIND"] = "0.0.0.0"
	m["CLOUD0_DB_PATH"] = "/tmp/x.db"
	m["CLOUD0_FIRST_WEEK"] = "2026-W20"
	m["CLOUD0_SETTLE_HOURS"] = "72"
	m["CLOUD0_PARTITION_MARGIN_DAYS"] = "2"
	m["CLOUD0_LOG_LEVEL"] = "DEBUG"
	m["CLOUD0_SMOKE_WEEK"] = "2026-W40"
	m["CLOUD0_E2E_PORT"] = "8111"
	m["CLOUD0_CURRENCY"] = "usd"
	cfg, err := Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CredentialsPath != "/outside/repo/key.json" || cfg.BillingAccountID != "XXXXXX-XXXXXX-XXXXXX" {
		t.Errorf("strings not read: %+v", cfg)
	}
	if cfg.Port != 9000 || cfg.Bind != "0.0.0.0" || cfg.DBPath != "/tmp/x.db" {
		t.Errorf("net/db not read: %+v", cfg)
	}
	if cfg.FirstWeek.String() != "2026-W20" || cfg.SettleHours != 72 || cfg.PartitionMarginDays != 2 {
		t.Errorf("week/settle not read: %+v", cfg)
	}
	if cfg.LogLevel != "debug" || cfg.SmokeWeek.String() != "2026-W40" || cfg.E2EPort != 8111 || cfg.Currency != "USD" {
		t.Errorf("level/smoke/e2e not read: %+v", cfg)
	}
}

func TestLoad_FallsBackToPORTWhenCLOUD0_PORTUnset(t *testing.T) {
	m := minimal()
	m["PORT"] = "8080"
	cfg, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 {
		t.Fatalf("Port = %d, want 8080 from PORT", cfg.Port)
	}
	m["CLOUD0_PORT"] = "8087"
	cfg, err = Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8087 {
		t.Fatalf("CLOUD0_PORT should win over PORT, got %d", cfg.Port)
	}
}

func TestLoad_ReportsEveryMissingRequiredVariable(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected error for empty env")
	}
	msg := err.Error()
	for _, v := range []string{"CLOUD0_GCP_PROJECTS", "CLOUD0_BILLING_EXPORT_TABLE", "CLOUD0_BIGQUERY_PROJECT"} {
		if !strings.Contains(msg, v) {
			t.Errorf("error does not name %s: %s", v, msg)
		}
	}
	if !strings.Contains(msg, ".env.example") {
		t.Errorf("error should point at .env.example: %s", msg)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("error is not a *ValidationError chain: %T", err)
	}
}

func TestLoad_RejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"table missing dataset", "CLOUD0_BILLING_EXPORT_TABLE", "project.table"},
		{"table with backticks", "CLOUD0_BILLING_EXPORT_TABLE", "`p.d.t`"},
		{"table with spaces", "CLOUD0_BILLING_EXPORT_TABLE", "p.d.t x"},
		{"port not numeric", "CLOUD0_PORT", "eighty"},
		{"port zero", "CLOUD0_PORT", "0"},
		{"port too big", "CLOUD0_PORT", "70000"},
		{"first week malformed", "CLOUD0_FIRST_WEEK", "2026-18"},
		{"first week nonexistent", "CLOUD0_FIRST_WEEK", "2027-W53"},
		{"settle negative", "CLOUD0_SETTLE_HOURS", "-1"},
		{"settle not int", "CLOUD0_SETTLE_HOURS", "2d"},
		{"margin negative", "CLOUD0_PARTITION_MARGIN_DAYS", "-2"},
		{"log level unknown", "CLOUD0_LOG_LEVEL", "verbose"},
		{"smoke week bad", "CLOUD0_SMOKE_WEEK", "W42"},
		{"currency too long", "CLOUD0_CURRENCY", "RUPEES"},
		{"currency digits", "CLOUD0_CURRENCY", "I2R"},
		{"currency symbol", "CLOUD0_CURRENCY", "₹"},
		{"e2e port bad", "CLOUD0_E2E_PORT", "99999"},
	}
	for _, c := range cases {
		m := minimal()
		m[c.key] = c.value
		_, err := Load(env(m))
		if err == nil {
			t.Errorf("%s: accepted %s=%q", c.name, c.key, c.value)
			continue
		}
		if !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s: error does not name %s: %v", c.name, c.key, err)
		}
	}
}

func TestLoad_TrimsAndDropsEmptyProjectEntries(t *testing.T) {
	m := minimal()
	m["CLOUD0_GCP_PROJECTS"] = " a ,, b ,"
	cfg, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 2 || cfg.Projects[0] != "a" || cfg.Projects[1] != "b" {
		t.Fatalf("Projects = %q", cfg.Projects)
	}
	m["CLOUD0_GCP_PROJECTS"] = " , "
	if _, err := Load(env(m)); err == nil {
		t.Fatal("whitespace-only project list accepted")
	}
}

func TestRedacted_NeverExposesTheCredentialsPath(t *testing.T) {
	m := minimal()
	m["GOOGLE_APPLICATION_CREDENTIALS"] = "/very/secret/location/key.json"
	cfg, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Redacted()
	if r["credentials"] != "set" {
		t.Errorf("credentials = %v, want \"set\"", r["credentials"])
	}
	for k, v := range r {
		if s, ok := v.(string); ok && strings.Contains(s, "secret") {
			t.Errorf("redacted view leaks the credentials path under %q", k)
		}
	}
	delete(m, "GOOGLE_APPLICATION_CREDENTIALS")
	cfg, _ = Load(env(m))
	if cfg.Redacted()["credentials"] != "unset (ADC)" {
		t.Errorf("unset credentials should read \"unset (ADC)\"")
	}
}

func TestLoad_ProposerSettings(t *testing.T) {
	cfg, err := Load(env(minimal()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenRouterAPIKey != "" || cfg.LLMModel != "anthropic/claude-sonnet-4-6" {
		t.Errorf("defaults wrong: key set=%v model=%q", cfg.OpenRouterAPIKey != "", cfg.LLMModel)
	}
	if cfg.Redacted()["openrouter_api_key"] != "unset" {
		t.Errorf("unset key should read \"unset\"")
	}

	m := minimal()
	m["CLOUD0_OPENROUTER_API_KEY"] = "  placeholder-not-a-real-key  "
	m["CLOUD0_LLM_MODEL"] = "vendor/model-x"
	cfg, err = Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenRouterAPIKey != "placeholder-not-a-real-key" || cfg.LLMModel != "vendor/model-x" {
		t.Errorf("not read/trimmed: model=%q", cfg.LLMModel)
	}
	r := cfg.Redacted()
	if r["openrouter_api_key"] != "set" || r["llm_model"] != "vendor/model-x" {
		t.Errorf("redacted view wrong: %v", r)
	}
	for k, v := range r {
		if s, ok := v.(string); ok && strings.Contains(s, "placeholder-not-a-real-key") {
			t.Errorf("redacted view leaks the API key under %q", k)
		}
	}
}
