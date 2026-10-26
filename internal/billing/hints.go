package billing

import (
	"context"
	"errors"
	"strings"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/googleapi"
)

// Code is the run_sources.error_code vocabulary (spec/capabilities/run-log.md).
type Code string

const (
	CodeCredentialsMissing Code = "credentials_missing"
	CodePermissionDenied   Code = "permission_denied"
	CodeAPIDisabled        Code = "api_disabled"
	CodeTableNotFound      Code = "table_not_found"
	CodeQuotaExceeded      Code = "quota_exceeded"
	CodeTimeout            Code = "timeout"
	CodeInvalidQuery       Code = "invalid_query"
	CodeUnknown            Code = "unknown"
)

// MaxMessageLen bounds Failure.Message (run_sources.error_message).
const MaxMessageLen = 2000

// hints are the fix instructions from spec/capabilities/run-log.md. They name
// environment variables and roles only — never a real value or path.
var hints = map[Code]string{
	CodeCredentialsMissing: "Set GOOGLE_APPLICATION_CREDENTIALS in .env to the absolute path of the service-account key (outside the repo), or run on a host with an attached service account.",
	CodePermissionDenied:   "Grant roles/bigquery.jobUser on CLOUD0_BIGQUERY_PROJECT and BigQuery Data Viewer on the export dataset to the service account.",
	CodeAPIDisabled:        "Enable bigquery.googleapis.com on CLOUD0_BIGQUERY_PROJECT.",
	CodeTableNotFound:      "Check CLOUD0_BILLING_EXPORT_TABLE (project.dataset.table) — the detailed (resource-level) billing export table.",
	CodeQuotaExceeded:      "BigQuery quota exceeded; wait and retry. Refresh one week instead of all weeks.",
	CodeTimeout:            "The query exceeded 120 s; retry, or refresh a single week.",
	CodeInvalidQuery:       "The generated SQL was rejected — this is a cloud0 bug; copy the SQL from the run log into an issue.",
	CodeUnknown:            "See error_message; check server logs with the request id.",
}

// hedgedExistenceText is BigQuery's wording when the caller lacks access to
// the dataset: it will not disclose whether the table exists, so a misspelled
// dataset in CLOUD0_BILLING_EXPORT_TABLE surfaces as this 403, not a 404.
const hedgedExistenceText = "perhaps it does not exist"

// hedgedExistenceHint is appended to the permission_denied hint in that case.
const hedgedExistenceHint = " BigQuery reports this same error when the dataset or table in CLOUD0_BILLING_EXPORT_TABLE is misspelled — check that name too."

// Hint returns the fix instruction for a code ("" for an unknown code).
func Hint(code Code) string { return hints[code] }

// Failure is a classified source error: the code stored in run_sources,
// the provider message (trimmed, never containing credential material) and
// the fix hint shown to the user.
type Failure struct {
	Code    Code
	Message string
	Hint    string
}

// Classify maps any error from the billing source to a Failure. A nil error
// yields the zero Failure.
func Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	code := classify(err)
	hint := hints[code]
	if code == CodePermissionDenied && strings.Contains(strings.ToLower(err.Error()), hedgedExistenceText) {
		hint += hedgedExistenceHint
	}
	return Failure{Code: code, Message: trim(err.Error(), MaxMessageLen), Hint: hint}
}

func classify(err error) Code {
	msg := err.Error()
	lower := strings.ToLower(msg)

	// Deadline first: a timed-out request can surface as a wrapped transport error.
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout exceeded") {
		return CodeTimeout
	}
	if errors.Is(err, ErrCredentials) || isCredentialText(lower) {
		return CodeCredentialsMissing
	}

	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return classifyHTTP(gerr, lower)
	}
	if bqErr, ok := asBigQueryError(err); ok {
		if c := classifyReason(bqErr.Reason, strings.ToLower(bqErr.Message)); c != "" {
			return c
		}
	}

	// gRPC-style or plain-text statuses.
	switch {
	case strings.Contains(msg, "SERVICE_DISABLED") || strings.Contains(lower, "has not been used in project"):
		return CodeAPIDisabled
	case strings.Contains(msg, "PERMISSION_DENIED"):
		return CodePermissionDenied
	case strings.Contains(msg, "RESOURCE_EXHAUSTED"):
		return CodeQuotaExceeded
	}
	return CodeUnknown
}

func classifyHTTP(gerr *googleapi.Error, lower string) Code {
	body := strings.ToLower(gerr.Body)
	if strings.Contains(gerr.Body, "SERVICE_DISABLED") || strings.Contains(lower, "has not been used in project") || strings.Contains(body, "has not been used in project") {
		return CodeAPIDisabled
	}
	for _, item := range gerr.Errors {
		if c := classifyReason(item.Reason, strings.ToLower(item.Message)); c != "" {
			return c
		}
	}
	switch gerr.Code {
	case 401:
		return CodeCredentialsMissing
	case 403:
		return CodePermissionDenied
	case 404:
		return CodeTableNotFound
	case 429:
		return CodeQuotaExceeded
	case 400:
		return CodeInvalidQuery
	}
	return CodeUnknown
}

// classifyReason maps a BigQuery error reason (the typed code on
// googleapi.ErrorItem / bigquery.Error) to a Code; "" when not recognised.
func classifyReason(reason, lowerMsg string) Code {
	switch reason {
	case "notFound":
		return CodeTableNotFound
	case "accessDenied", "forbidden":
		return CodePermissionDenied
	case "accessNotConfigured":
		return CodeAPIDisabled
	case "quotaExceeded", "rateLimitExceeded", "responseTooLarge", "billingTierLimitExceeded":
		return CodeQuotaExceeded
	case "invalidQuery":
		return CodeInvalidQuery
	case "invalid":
		if strings.Contains(lowerMsg, "not found") {
			return CodeTableNotFound
		}
		return CodeInvalidQuery
	case "authError", "invalidCredentials":
		return CodeCredentialsMissing
	}
	return ""
}

func asBigQueryError(err error) (*bigquery.Error, bool) {
	var ptr *bigquery.Error
	if errors.As(err, &ptr) {
		return ptr, true
	}
	var val bigquery.Error
	if errors.As(err, &val) {
		return &val, true
	}
	return nil, false
}

func isCredentialText(lower string) bool {
	for _, needle := range []string{
		"could not find default credentials",
		"invalid_grant",
		"invalid authentication credentials",
		"cannot read credentials file",
		"error getting credentials",
		"unable to read credentials",
		"credentials file",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
