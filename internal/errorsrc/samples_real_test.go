package errorsrc

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/monitoring"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// TestSampleErrors_RealAPI_BusiestServiceW42 finds the Cloud Run service with
// the most 4xx+5xx in 2026-W42 (Monitoring), then samples its failing requests
// from real Cloud Logging. Skips only when credentials are genuinely absent.
func TestSampleErrors_RealAPI_BusiestServiceW42(t *testing.T) {
	testenv.LoadDotEnv(t)
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" || os.Getenv("CLOUD0_GCP_PROJECTS") == "" {
		t.Skip("GOOGLE_APPLICATION_CREDENTIALS or CLOUD0_GCP_PROJECTS not set — fill .env (see .env.example) to run real Logging tests")
	}
	var projects []string
	for _, p := range strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	l, closeMon, err := monitoring.NewLister(ctx)
	if err != nil {
		t.Fatalf("NewLister: %v", err)
	}
	defer closeMon()
	res := FetchCloudRun(ctx, l, projects, crW42, time.Now)
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("Status = %q: %s", res.Status, res.Message)
	}
	var best gcpsource.ErrorRow
	var bestN int64 = -1
	for _, r := range res.Errors {
		if r.Gap != "" || r.Service == "" {
			continue
		}
		if n := *r.C4xx + *r.C5xx; n > bestN {
			best, bestN = r, n
		}
	}
	if bestN <= 0 {
		t.Fatal("no Cloud Run service with 4xx/5xx in W42")
	}
	if len(best.ByCode) == 0 {
		t.Fatalf("busiest service has no by_code map: %+v", best)
	}
	reader, closeLogs, err := NewSampleReader(ctx, projects)
	if err != nil {
		t.Fatalf("NewSampleReader: %v", err)
	}
	defer closeLogs()
	sr, err := SampleErrors(ctx, reader, SampleParams{Project: best.Project, Service: best.Service, Window: crW42, Limit: 20, Now: time.Now()})
	if err != nil {
		t.Fatalf("SampleErrors: %v", err)
	}
	if sr.OutOfRetention {
		t.Fatalf("W42 should still be inside the 30-day retention: %+v", sr)
	}
	t.Logf("busiest service: 4xx+5xx=%d scanned=%d top_paths=%d scanner_share=%v entries=%d", bestN, sr.Scanned, len(sr.TopPaths), sr.ScannerShare4xx, len(sr.Entries))
	if sr.Scanned == 0 || len(sr.TopPaths) == 0 {
		t.Fatalf("scanned=%d top_paths=%d, want both > 0", sr.Scanned, len(sr.TopPaths))
	}
	for _, e := range sr.Entries {
		if strings.Count(e.RemoteIP, ".") == 3 && !strings.HasSuffix(e.RemoteIP, ".0/24") {
			t.Fatalf("IP not truncated: %q", e.RemoteIP)
		}
	}
}
