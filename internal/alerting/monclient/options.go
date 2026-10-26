// Package monclient implements the Cloud Monitoring side of the Alerts feature
// (alert policies and notification channels) over the official gRPC clients.
//
// Reads use the Reader options (cloud0-reader or ADC); every write uses the
// Writer options (cloud0-alerts). A nil Writer means alerts credentials are not
// configured and every write method returns alerting.ErrNotConfigured.
package monclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Creds carries the client options of the two service accounts.
type Creds struct {
	Reader []option.ClientOption // nil = Application Default Credentials
	Writer []option.ClientOption // nil = alerts credentials not configured
}

// EnvAlertsCredentials is the env var holding the cloud0-alerts key path.
const EnvAlertsCredentials = "CLOUD0_ALERTS_CREDENTIALS"

// CredsFromEnv builds Creds: Reader is left to ADC, Writer comes from the key
// file named by CLOUD0_ALERTS_CREDENTIALS (nil when unset). The path is never
// logged.
func CredsFromEnv() Creds {
	var c Creds
	if p := strings.TrimSpace(os.Getenv(EnvAlertsCredentials)); p != "" {
		c.Writer = []option.ClientOption{option.WithCredentialsFile(p)}
	}
	return c
}

// Roles named in permission_denied messages.
const (
	roleViewer         = "roles/monitoring.viewer"
	roleAlertEditor    = "roles/monitoring.alertPolicyEditor"
	roleChannelEditor  = "roles/monitoring.notificationChannelEditor"
	policyNamePattern  = "projects/{project}/alertPolicies/{id}"
	channelNamePattern = "projects/{project}/notificationChannels/{id}"
)

// mapErr converts a gRPC/API error into an alerting sentinel wrapped with a
// readable message. write selects the role named for permission errors.
func mapErr(err error, write bool, editorRole, project string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", alerting.ErrUpstream, err)
	}
	msg := st.Message()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "has not been used in project") || strings.Contains(lower, "service_disabled") ||
		strings.Contains(lower, "api has not been enabled") || strings.Contains(lower, "is disabled") && strings.Contains(lower, "api") {
		return fmt.Errorf("%w: monitoring.googleapis.com is not enabled (gcloud services enable monitoring.googleapis.com --project=%s): %s",
			alerting.ErrAPIDisabled, project, msg)
	}
	switch st.Code() {
	case codes.PermissionDenied, codes.Unauthenticated:
		role := roleViewer
		if write {
			role = editorRole
		}
		if strings.Contains(lower, "logging.notificationrules") {
			// Log-match alert conditions are also checked against Cloud Logging IAM.
			role = "the logging.notificationRules.* permissions (roles/logging.configWriter, or a custom role holding only those)"
		}
		return fmt.Errorf("%w: grant %s to the service account on %s: %s", alerting.ErrPermissionDenied, role, project, msg)
	case codes.NotFound:
		return fmt.Errorf("%w: %s", alerting.ErrNotFound, msg)
	case codes.InvalidArgument, codes.OutOfRange, codes.AlreadyExists:
		return fmt.Errorf("%w: %s", alerting.ErrInvalidParams, msg)
	}
	return fmt.Errorf("%w: %s: %s", alerting.ErrUpstream, st.Code(), msg)
}

// projectOf extracts "proj-a" from "projects/proj-a/...".
func projectOf(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) >= 2 && parts[0] == "projects" {
		return parts[1]
	}
	return ""
}

// validName checks "projects/<p>/<collection>/<id>".
func validName(name, collection string) bool {
	parts := strings.Split(name, "/")
	return len(parts) == 4 && parts[0] == "projects" && parts[1] != "" && parts[2] == collection && parts[3] != ""
}
