package labeler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

// EnvCredentials is the path of the labels-writer service-account key.
const EnvCredentials = "CLOUD0_LABELS_CREDENTIALS"

const (
	scopeCloudPlatform = "https://www.googleapis.com/auth/cloud-platform"
	pollEvery          = 700 * time.Millisecond
	pollFor            = 90 * time.Second
)

// REST implements Client over the public REST APIs.
type REST struct {
	hc   *http.Client
	base map[string]string // overridable per API for tests
}

// NewREST builds a client from a service-account key file.
func NewREST(ctx context.Context, keyPath string) (*REST, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", EnvCredentials, err)
	}
	creds, err := google.CredentialsFromJSON(ctx, raw, scopeCloudPlatform)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", EnvCredentials, err)
	}
	hc := newAuthedClient(ctx, creds)
	return NewRESTWithClient(hc), nil
}

// NewRESTWithClient is for tests: any http.Client, default base URLs.
func NewRESTWithClient(hc *http.Client) *REST {
	return &REST{hc: hc, base: map[string]string{
		"compute": "https://compute.googleapis.com/compute/v1",
		"run":     "https://run.googleapis.com/v2",
		"sql":     "https://sqladmin.googleapis.com/sql/v1beta4",
		"gcs":     "https://storage.googleapis.com/storage/v1",
	}}
}

// WithBase overrides one API's base URL (tests).
func (c *REST) WithBase(api, base string) *REST {
	c.base[api] = strings.TrimRight(base, "/")
	return c
}

// ---- HTTP plumbing -----------------------------------------------------------

func (c *REST) do(ctx context.Context, method, u string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return &Error{Code: CodeFailed, Message: err.Error()}
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return &Error{Code: CodeFailed, Message: err.Error()}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return &Error{Code: CodeFailed, Message: "request failed: " + trim(err.Error(), 200), Hint: "Check network access to Google APIs and try again."}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return classify(resp.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &Error{Code: CodeFailed, Message: "unexpected response: " + trim(err.Error(), 200)}
		}
	}
	return nil
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

type googleErr struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

func classify(status int, raw []byte) *Error {
	var g googleErr
	_ = json.Unmarshal(raw, &g)
	msg := g.Error.Message
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	msg = trim(msg, 300)
	reason := ""
	for _, e := range g.Error.Errors {
		reason = e.Reason
	}
	for _, e := range g.Error.Details {
		if e.Reason != "" {
			reason = e.Reason
		}
	}
	e := &Error{Code: CodeFailed, Message: msg, Status: status}
	switch {
	case reason == "SERVICE_DISABLED" || strings.Contains(msg, "has not been used in project") || strings.Contains(msg, "API has not been enabled"):
		e.Code, e.Hint = CodeAPIOff, "Enable the API in the project (gcloud services enable <api>) and retry."
	case status == http.StatusNotFound:
		e.Code, e.Hint = CodeNotFound, "The billing resource name may not match the live resource (renamed or deleted); label it in the console instead."
	case strings.Contains(msg, "iam.serviceaccounts.actAs"):
		e.Code = CodePermission
		e.Hint = "Cloud Run needs act-as on the runtime service account for any update, even labels. cloud0 deliberately does not hold that (it would let it run code as your service accounts): apply the gcloud or Terraform line from the preview instead."
	case status == http.StatusForbidden || status == http.StatusUnauthorized:
		e.Code, e.Hint = CodePermission, "Grant the labels-writer service account the cloud0 label-writer role on this project (see README → Labels setup)."
	case status == http.StatusConflict || status == http.StatusPreconditionFailed:
		e.Code, e.Hint = CodeConflict, "The resource changed while editing; preview again and retry."
	case status == http.StatusBadRequest:
		e.Code, e.Hint = CodeInvalid, "GCP rejected the request; check the label values."
	}
	return e
}

// ---- Client implementation ---------------------------------------------------

func (c *REST) Get(ctx context.Context, t Target) (map[string]string, error) {
	if err := ValidTarget(t); err != nil {
		return nil, &Error{Code: CodeInvalid, Message: err.Error()}
	}
	labels, _, err := c.read(ctx, t)
	return labels, err
}

// read returns the labels and the concurrency token (fingerprint/etag) when the API has one.
func (c *REST) read(ctx context.Context, t Target) (map[string]string, string, error) {
	var obj struct {
		Labels           map[string]string `json:"labels"`
		LabelFingerprint string            `json:"labelFingerprint"`
		Etag             string            `json:"etag"`
		Settings         struct {
			UserLabels map[string]string `json:"userLabels"`
		} `json:"settings"`
	}
	if err := c.do(ctx, http.MethodGet, c.resourceURL(t), nil, &obj); err != nil {
		return nil, "", err
	}
	switch t.Type {
	case TypeCloudSQLInstance:
		return nz(obj.Settings.UserLabels), "", nil
	case TypeComputeInstance, TypeComputeDisk:
		return nz(obj.Labels), obj.LabelFingerprint, nil
	default:
		return nz(obj.Labels), obj.Etag, nil
	}
}

func nz(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (c *REST) resourceURL(t Target) string {
	p, n, l := url.PathEscape(t.Project), url.PathEscape(t.Name), url.PathEscape(t.Location)
	switch t.Type {
	case TypeComputeInstance:
		return fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s", c.base["compute"], p, l, n)
	case TypeComputeDisk:
		return fmt.Sprintf("%s/projects/%s/zones/%s/disks/%s", c.base["compute"], p, l, n)
	case TypeCloudRunService:
		return fmt.Sprintf("%s/projects/%s/locations/%s/services/%s", c.base["run"], p, l, n)
	case TypeCloudRunJob:
		return fmt.Sprintf("%s/projects/%s/locations/%s/jobs/%s", c.base["run"], p, l, n)
	case TypeCloudSQLInstance:
		return fmt.Sprintf("%s/projects/%s/instances/%s", c.base["sql"], p, n)
	case TypeGCSBucket:
		return fmt.Sprintf("%s/b/%s", c.base["gcs"], n)
	}
	return ""
}

func (c *REST) Merge(ctx context.Context, t Target, add map[string]string) (map[string]string, error) {
	if err := ValidTarget(t); err != nil {
		return nil, &Error{Code: CodeInvalid, Message: err.Error()}
	}
	for k, v := range add {
		if !ValidLabel(k, v) {
			return nil, &Error{Code: CodeInvalid, Message: fmt.Sprintf("label %q=%q is not valid (lowercase letters, digits, _ and -; max 63; key starts with a letter)", k, v)}
		}
	}
	cur, token, err := c.read(ctx, t)
	if err != nil {
		return nil, err
	}
	merged := Merged(cur, add)
	u := c.resourceURL(t)
	ctxOp, cancel := context.WithTimeout(ctx, pollFor)
	defer cancel()

	switch t.Type {
	case TypeComputeInstance, TypeComputeDisk:
		var op struct {
			SelfLink string `json:"selfLink"`
			Status   string `json:"status"`
			Error    *struct {
				Errors []struct{ Message string } `json:"errors"`
			} `json:"error"`
		}
		body := map[string]any{"labels": merged, "labelFingerprint": token}
		if err := c.do(ctx, http.MethodPost, u+"/setLabels", body, &op); err != nil {
			return nil, err
		}
		if err := c.pollCompute(ctxOp, op.SelfLink, op.Status); err != nil {
			return nil, err
		}
	case TypeCloudRunService, TypeCloudRunJob:
		var op struct {
			Name  string `json:"name"`
			Done  bool   `json:"done"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if t.Type == TypeCloudRunService {
			// services.patch honours updateMask: only labels change.
			if err := c.do(ctx, http.MethodPatch, u+"?updateMask=labels", map[string]any{"labels": merged}, &op); err != nil {
				return nil, err
			}
		} else {
			// jobs.patch has no mask: send the whole job back with the new labels.
			var full map[string]any
			if err := c.do(ctx, http.MethodGet, u, nil, &full); err != nil {
				return nil, err
			}
			full["labels"] = merged
			if err := c.do(ctx, http.MethodPatch, u, full, &op); err != nil {
				return nil, err
			}
		}
		msg := ""
		if op.Error != nil {
			msg = op.Error.Message
		}
		if err := c.pollRun(ctxOp, op.Name, op.Done, op.Error != nil, msg); err != nil {
			return nil, err
		}
	case TypeCloudSQLInstance:
		var op struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Error  *struct {
				Errors []struct{ Message string } `json:"errors"`
			} `json:"error"`
		}
		body := map[string]any{"settings": map[string]any{"userLabels": merged}}
		if err := c.do(ctx, http.MethodPatch, u, body, &op); err != nil {
			return nil, err
		}
		if err := c.pollSQL(ctxOp, t.Project, op.Name, op.Status); err != nil {
			return nil, err
		}
	case TypeGCSBucket:
		// buckets.patch merges label keys.
		body := map[string]any{"labels": add}
		if err := c.do(ctx, http.MethodPatch, u, body, nil); err != nil {
			return nil, err
		}
	}
	after, _, err := c.read(ctx, t)
	if err != nil {
		return nil, err
	}
	for k, v := range add {
		if after[k] != v {
			return nil, &Error{Code: CodeFailed, Message: fmt.Sprintf("the write was accepted but label %s did not read back as %q", k, v), Hint: "Preview again to see the live labels."}
		}
	}
	return after, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return &Error{Code: CodeFailed, Message: "timed out waiting for the operation to finish", Hint: "The change may still complete; preview again to check the live labels."}
	case <-time.After(d):
		return nil
	}
}

func (c *REST) pollCompute(ctx context.Context, selfLink, status string) error {
	for status != "DONE" {
		if err := sleepCtx(ctx, pollEvery); err != nil {
			return err
		}
		var op struct {
			Status string `json:"status"`
			Error  *struct {
				Errors []struct{ Message string } `json:"errors"`
			} `json:"error"`
		}
		if err := c.do(ctx, http.MethodGet, selfLink, nil, &op); err != nil {
			return err
		}
		status = op.Status
		if op.Error != nil && len(op.Error.Errors) > 0 {
			return &Error{Code: CodeFailed, Message: trim(op.Error.Errors[0].Message, 300)}
		}
	}
	return nil
}

func (c *REST) pollRun(ctx context.Context, name string, done, failed bool, msg string) error {
	if failed {
		return &Error{Code: CodeFailed, Message: trim(msg, 300)}
	}
	for !done {
		if err := sleepCtx(ctx, pollEvery); err != nil {
			return err
		}
		var op struct {
			Done  bool `json:"done"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := c.do(ctx, http.MethodGet, c.base["run"]+"/"+name, nil, &op); err != nil {
			return err
		}
		if op.Error != nil {
			return &Error{Code: CodeFailed, Message: trim(op.Error.Message, 300)}
		}
		done = op.Done
	}
	return nil
}

func (c *REST) pollSQL(ctx context.Context, project, name, status string) error {
	for status != "DONE" {
		if err := sleepCtx(ctx, pollEvery); err != nil {
			return err
		}
		var op struct {
			Status string `json:"status"`
			Error  *struct {
				Errors []struct{ Message string } `json:"errors"`
			} `json:"error"`
		}
		u := fmt.Sprintf("%s/projects/%s/operations/%s", c.base["sql"], url.PathEscape(project), url.PathEscape(name))
		if err := c.do(ctx, http.MethodGet, u, nil, &op); err != nil {
			return err
		}
		status = op.Status
		if op.Error != nil && len(op.Error.Errors) > 0 {
			return &Error{Code: CodeFailed, Message: trim(op.Error.Errors[0].Message, 300)}
		}
	}
	return nil
}
