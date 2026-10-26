package gcpsource

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{status.Error(codes.PermissionDenied, "denied"), StatusPermissionDenied},
		{errors.New("SERVICE_DISABLED: Cloud Monitoring API has not been used in project x"), StatusAPIDisabled},
		{status.Error(codes.ResourceExhausted, "slow down"), StatusQuota},
		{errors.New("boom"), StatusFailed},
	}
	for _, c := range cases {
		got, _, hint := Classify(c.err, "roles/monitoring.viewer", "monitoring.googleapis.com")
		if got != c.want {
			t.Errorf("Classify(%v) = %s, want %s", c.err, got, c.want)
		}
		if got == StatusPermissionDenied && hint != "grant the service account roles/monitoring.viewer" {
			t.Errorf("hint = %q", hint)
		}
	}
}
