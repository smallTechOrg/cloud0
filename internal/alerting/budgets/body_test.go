package budgets

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update): %v", name, err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(got)) {
		t.Fatalf("golden %s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

const ch = "projects/proj-a/notificationChannels/111"

func TestBody_AccountBudgetGolden(t *testing.T) {
	got, err := Body(alerting.BudgetDraft{DisplayName: "cloud0-account-monthly", Amount: 6500, Projects: []string{"proj-a", "projects/123456"}}, []string{ch}, "INR")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "account.golden.json", got)
}

func TestBody_ServiceBudgetGolden(t *testing.T) {
	f := false
	got, err := Body(alerting.BudgetDraft{
		DisplayName: "cloud0-compute-engine-monthly", Amount: 1234.5, ServiceIDs: []string{"services/AAAA-BBBB-CCCC", "DDDD-EEEE-FFFF"},
		Thresholds:           []alerting.BudgetThreshold{{Percent: 90, Basis: BasisCurrent}, {Percent: 110, Basis: BasisForecast}},
		DefaultIAMRecipients: &f,
	}, []string{ch, "projects/proj-a/notificationChannels/222"}, "INR")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "service.golden.json", got)
}

func TestBody_DefaultThresholdsAndNoChannels(t *testing.T) {
	got, err := Body(alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 100}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{`"thresholdPercent": 0.5`, `"thresholdPercent": 0.8`, `"FORECASTED_SPEND"`, `"CURRENT_SPEND"`, `"calendarPeriod": "MONTH"`,
		`"creditTypesTreatment": "INCLUDE_ALL_CREDITS"`, `"currencyCode": "INR"`, `"schemaVersion": "1.0"`, `"disableDefaultIamRecipients": false`} {
		if !strings.Contains(s, want) {
			t.Errorf("body lacks %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "monitoringNotificationChannels") || strings.Contains(s, `"projects"`) || strings.Contains(s, `"services"`) {
		t.Errorf("empty lists must be omitted:\n%s", s)
	}
}

func TestBody_Rejections(t *testing.T) {
	six := make([]string, 6)
	for i := range six {
		six[i] = "projects/proj-a/notificationChannels/" + string(rune('1'+i))
	}
	f := false
	cases := map[string]struct {
		d  alerting.BudgetDraft
		ch []string
	}{
		"more than 5 channels":         {alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 1}, six},
		"zero amount":                  {alerting.BudgetDraft{DisplayName: "cloud0-x"}, nil},
		"negative amount":              {alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: -5}, nil},
		"empty name":                   {alerting.BudgetDraft{Amount: 1}, nil},
		"bad channel name":             {alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 1}, []string{"nope"}},
		"bad basis":                    {alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 1, Thresholds: []alerting.BudgetThreshold{{Percent: 50, Basis: "x"}}}, nil},
		"zero percent":                 {alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 1, Thresholds: []alerting.BudgetThreshold{{Percent: 0, Basis: BasisCurrent}}}, nil},
		"IAM disabled with no channel": {alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 1, DefaultIAMRecipients: &f}, nil},
		"empty project":                {alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 1, Projects: []string{" "}}, nil},
	}
	for name, c := range cases {
		if _, err := Body(c.d, c.ch, "INR"); !errors.Is(err, alerting.ErrInvalidParams) {
			t.Errorf("%s: err = %v, want ErrInvalidParams", name, err)
		}
	}
	// exactly 5 is fine
	if _, err := Body(alerting.BudgetDraft{DisplayName: "cloud0-x", Amount: 1}, six[:5], "INR"); err != nil {
		t.Errorf("5 channels: %v", err)
	}
}
