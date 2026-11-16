// Package labeler reads and writes GCP resource labels (Compute instances and
// disks, Cloud Run services and jobs, Cloud SQL instances, GCS buckets) so
// cloud0 can make `product` / `env` attribution authoritative at the source.
//
// It only ever ADDS or CHANGES the labels it is given: every other label on
// the resource is read first and written back unchanged.
package labeler

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Resource types (match internal/attribution Type* constants).
const (
	TypeComputeInstance  = "compute_instance"
	TypeComputeDisk      = "compute_disk"
	TypeCloudRunService  = "cloud_run_service"
	TypeCloudRunJob      = "cloud_run_job"
	TypeGCSBucket        = "gcs_bucket"
	TypeCloudSQLInstance = "cloud_sql_instance"
)

// Target identifies one labelled GCP resource.
type Target struct {
	Project  string `json:"project"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
}

func (t Target) String() string {
	loc := ""
	if t.Location != "" {
		loc = "/" + t.Location
	}
	return fmt.Sprintf("%s:%s%s/%s", t.Type, t.Project, loc, t.Name)
}

// Client reads and writes labels.
type Client interface {
	// Get returns the resource's current labels.
	Get(ctx context.Context, t Target) (map[string]string, error)
	// Merge writes add on top of the current labels (nothing else changes),
	// waits for the operation, and returns the labels as re-read afterwards.
	Merge(ctx context.Context, t Target, add map[string]string) (map[string]string, error)
}

// Error codes of *Error.
const (
	CodeNotFound   = "resource_not_found"
	CodePermission = "permission_denied"
	CodeAPIOff     = "api_disabled"
	CodeConflict   = "conflict"
	CodeInvalid    = "invalid_target"
	CodeFailed     = "failed"
)

// Error is a classified failure with a fix hint.
type Error struct {
	Code    string
	Message string
	Hint    string
	Status  int
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Diff is the label change a Merge would make.
type Diff struct {
	Key    string  `json:"key"`
	Before *string `json:"before"`
	After  string  `json:"after"`
	Change string  `json:"change"` // add | change | same
}

// PlanDiff compares current labels with the labels to write; keys are sorted.
func PlanDiff(current, add map[string]string) []Diff {
	keys := make([]string, 0, len(add))
	for k := range add {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Diff, 0, len(keys))
	for _, k := range keys {
		d := Diff{Key: k, After: add[k]}
		if cur, ok := current[k]; ok {
			c := cur
			d.Before = &c
			if cur == add[k] {
				d.Change = "same"
			} else {
				d.Change = "change"
			}
		} else {
			d.Change = "add"
		}
		out = append(out, d)
	}
	return out
}

// Merged returns current overlaid with add.
func Merged(current, add map[string]string) map[string]string {
	out := make(map[string]string, len(current)+len(add))
	for k, v := range current {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

// ValidTarget checks the fields a Target needs for its type.
func ValidTarget(t Target) error {
	if t.Project == "" || t.Name == "" {
		return fmt.Errorf("project and name are required")
	}
	switch t.Type {
	case TypeComputeInstance, TypeComputeDisk, TypeCloudRunService, TypeCloudRunJob:
		if t.Location == "" {
			return fmt.Errorf("location (zone or region) is required for %s", t.Type)
		}
	case TypeGCSBucket, TypeCloudSQLInstance:
	default:
		return fmt.Errorf("unsupported resource type %q", t.Type)
	}
	return nil
}

// ValidLabel enforces GCP's label syntax: keys/values of [a-z0-9_-], keys start
// with a letter, at most 63 characters.
func ValidLabel(k, v string) bool {
	if k == "" || len(k) > 63 || len(v) > 63 {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Terraform renders the labels attribute for the Terraform resource that
// manages the target, with the full merged label set, so the change can be
// made in an infrastructure repo instead of (or as well as) through the API.
func Terraform(t Target, merged map[string]string) string {
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	attr := "labels"
	indent := "  "
	var res string
	switch t.Type {
	case TypeComputeInstance:
		res = "google_compute_instance"
	case TypeComputeDisk:
		res = "google_compute_disk"
	case TypeCloudRunService:
		res = "google_cloud_run_v2_service"
	case TypeCloudRunJob:
		res = "google_cloud_run_v2_job"
	case TypeGCSBucket:
		res = "google_storage_bucket"
	case TypeCloudSQLInstance:
		res = "google_sql_database_instance"
		attr = "user_labels"
		indent = "    "
	default:
		return ""
	}
	fmt.Fprintf(&b, "# %s (%s) — merge into the existing resource block\n", res, t.Name)
	if t.Type == TypeCloudSQLInstance {
		b.WriteString("settings {\n")
	}
	fmt.Fprintf(&b, "%s%s = {\n", indent[:len(indent)-2], attr)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s%s = %q\n", indent, k, merged[k])
	}
	fmt.Fprintf(&b, "%s}", indent[:len(indent)-2])
	if t.Type == TypeCloudSQLInstance {
		b.WriteString("\n}")
	}
	return b.String()
}
