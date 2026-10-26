package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
)

func gapi(code int, reason, message string) error {
	return &googleapi.Error{
		Code:    code,
		Message: message,
		Errors:  []googleapi.ErrorItem{{Reason: reason, Message: message}},
	}
}

func TestClassify_MapsProviderErrorsToTheSpecCodes(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode Code
		hintHas  string
	}{
		{"403 accessDenied", gapi(403, "accessDenied", "Access Denied: Table x: User does not have permission"), CodePermissionDenied, "roles/bigquery.jobUser"},
		{"403 PERMISSION_DENIED text", errors.New("rpc error: code = PermissionDenied desc = PERMISSION_DENIED"), CodePermissionDenied, "CLOUD0_BIGQUERY_PROJECT"},
		{"404 notFound", gapi(404, "notFound", "Not found: Table x"), CodeTableNotFound, "CLOUD0_BILLING_EXPORT_TABLE"},
		{"403 SERVICE_DISABLED in body", &googleapi.Error{Code: 403, Message: "BigQuery API has not been used in project 1 before or it is disabled.", Body: `{"error":{"status":"PERMISSION_DENIED","details":[{"reason":"SERVICE_DISABLED"}]}}`}, CodeAPIDisabled, "bigquery.googleapis.com"},
		{"403 accessNotConfigured", gapi(403, "accessNotConfigured", "Access Not Configured"), CodeAPIDisabled, "Enable"},
		{"403 quotaExceeded", gapi(403, "quotaExceeded", "Quota exceeded"), CodeQuotaExceeded, "one week"},
		{"403 rateLimitExceeded", gapi(403, "rateLimitExceeded", "Exceeded rate limits"), CodeQuotaExceeded, "retry"},
		{"429", &googleapi.Error{Code: 429, Message: "Too many requests"}, CodeQuotaExceeded, "quota"},
		{"400 invalidQuery", gapi(400, "invalidQuery", "Syntax error"), CodeInvalidQuery, "cloud0 bug"},
		{"bigquery.Error notFound", bigquery.Error{Reason: "notFound", Message: "Not found: Dataset"}, CodeTableNotFound, "CLOUD0_BILLING_EXPORT_TABLE"},
		{"*bigquery.Error accessDenied", &bigquery.Error{Reason: "accessDenied", Message: "denied"}, CodePermissionDenied, "service account"},
		{"*bigquery.Error invalidQuery", &bigquery.Error{Reason: "invalidQuery", Message: "bad"}, CodeInvalidQuery, "SQL"},
		{"context deadline", fmt.Errorf("billing: run query: %w", context.DeadlineExceeded), CodeTimeout, "120 s"},
		{"deadline text only", errors.New("Post \"https://x\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), CodeTimeout, "single week"},
		{"ErrCredentials", fmt.Errorf("%w: cannot read credentials file", ErrCredentials), CodeCredentialsMissing, "GOOGLE_APPLICATION_CREDENTIALS"},
		{"ADC missing text", errors.New("google: could not find default credentials. See https://cloud.google.com/docs/authentication/external/set-up-adc"), CodeCredentialsMissing, "GOOGLE_APPLICATION_CREDENTIALS"},
		{"401", &googleapi.Error{Code: 401, Message: "Request had invalid authentication credentials."}, CodeCredentialsMissing, ".env"},
		{"invalid_grant", errors.New("oauth2: \"invalid_grant\" \"Invalid JWT Signature.\""), CodeCredentialsMissing, "service-account key"},
		{"anything else", errors.New("boom"), CodeUnknown, "error_message"},
		{"403 without reason", &googleapi.Error{Code: 403, Message: "forbidden"}, CodePermissionDenied, "Grant"},
		// BigQuery will not disclose existence without dataset access: a
		// misspelled dataset arrives as this hedged 403, so the hint must
		// also point at the table variable.
		{"403 hedged existence", gapi(403, "accessDenied", "Access Denied: Table p:d.t: User does not have permission to query table p:d.t, or perhaps it does not exist."), CodePermissionDenied, "CLOUD0_BILLING_EXPORT_TABLE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Classify(c.err)
			if f.Code != c.wantCode {
				t.Fatalf("Code = %q, want %q (message %q)", f.Code, c.wantCode, f.Message)
			}
			if f.Message == "" {
				t.Error("Message is empty")
			}
			if !strings.Contains(f.Hint, c.hintHas) {
				t.Errorf("Hint %q does not mention %q", f.Hint, c.hintHas)
			}
		})
	}
}

func TestClassify_TrimsTheMessageTo2000Chars(t *testing.T) {
	long := strings.Repeat("x", 5000)
	f := Classify(errors.New(long))
	if len(f.Message) != MaxMessageLen {
		t.Fatalf("len(Message) = %d, want %d", len(f.Message), MaxMessageLen)
	}
	if f.Code != CodeUnknown {
		t.Fatalf("Code = %q, want unknown", f.Code)
	}
}

func TestClassify_NilIsNoFailure(t *testing.T) {
	f := Classify(nil)
	if f != (Failure{}) {
		t.Fatalf("Classify(nil) = %+v, want zero Failure", f)
	}
}

func TestClassify_EveryCodeHasAHintNamingTheFixNotAValue(t *testing.T) {
	for code, hint := range hints {
		if hint == "" {
			t.Errorf("code %q has no hint", code)
		}
		// Hints must refer to configuration by variable name only; no
		// filesystem paths or identifiers.
		if strings.Contains(hint, "/Users/") || strings.Contains(hint, ".json") {
			t.Errorf("code %q hint leaks a path: %q", code, hint)
		}
	}
	for _, code := range []Code{CodeCredentialsMissing, CodePermissionDenied, CodeAPIDisabled, CodeTableNotFound, CodeQuotaExceeded, CodeTimeout, CodeInvalidQuery, CodeUnknown} {
		if _, ok := hints[code]; !ok {
			t.Errorf("code %q missing from hints", code)
		}
	}
}
