package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// registerAttributionRightsizing wires GET /api/v1/rightsizing.
func (d Deps) registerAttributionRightsizing(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/rightsizing", d.handleRightsizing)
}

const (
	srcMonitoringVM     = "monitoring_vm"
	srcComputeInventory = "compute_inventory"
	srcBillingSnapshots = "billing_snapshots"
	maxRightsizingWeeks = 26
)

func rsErrNotConfigured() *APIError {
	return (&APIError{Status: http.StatusServiceUnavailable, Code: "attribution_not_configured",
		Message: "Attribution is not configured on this server."}).
		WithHint("Start cloud0 with the Phase 3 attribution dependencies wired (see spec/architecture.md).")
}

// rightsizingThresholds reads the rightsizing.* settings; invalid or absent
// values fall back to the defaults.
func (d Deps) rightsizingThresholds(ctx context.Context) (attribution.RightsizingThresholds, error) {
	th := attribution.DefaultRightsizingThresholds()
	kv, err := store.GetSettingsWithPrefix(ctx, d.Store.DB, "rightsizing.")
	if err != nil {
		return th, err
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(kv["rightsizing.cpu_pct"]), 64); err == nil && v > 0 && v <= 100 {
		th.CPUPct = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(kv["rightsizing.weeks"])); err == nil && v >= 1 && v <= maxRightsizingWeeks {
		th.Weeks = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(kv["rightsizing.stale_snapshot_days"])); err == nil && v >= 1 {
		th.StaleSnapshotDays = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(kv["rightsizing.idle_run_min_net"]), 64); err == nil && v >= 0 {
		th.IdleRunMinNet = v
	}
	return th, nil
}

func weekList(last isoweek.Week, n int) []isoweek.Week {
	ws := make([]isoweek.Week, n)
	cur := last
	for i := n - 1; i >= 0; i-- {
		ws[i] = cur
		cur = cur.Prev()
	}
	return ws
}

func weekNames(ws []isoweek.Week) string {
	s := make([]string, len(ws))
	for i, w := range ws {
		s[i] = w.String()
	}
	return strings.Join(s, ", ")
}

func okSource(name string) attribution.SourceStatus {
	return attribution.SourceStatus{Source: name, Status: "ok"}
}

func sourceWith(name, status, code, msg, hint string) attribution.SourceStatus {
	s := attribution.SourceStatus{Source: name, Status: status, Message: &msg}
	if code != "" {
		s.ErrorCode = &code
	}
	if hint != "" {
		s.FixHint = &hint
	}
	return s
}

func (d Deps) handleRightsizing(w http.ResponseWriter, r *http.Request) *APIError {
	if d.Attribution == nil {
		return rsErrNotConfigured()
	}
	ctx := r.Context()
	now := d.now()
	q := r.URL.Query()
	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	week, aerr := d.weekParam(q.Get("week"), now)
	if aerr != nil {
		return aerr
	}
	th, err := d.rightsizingThresholds(ctx)
	if err != nil {
		return ErrInternal(err)
	}
	weeks := weekList(week, th.Weeks)

	in := attribution.RightsizingInput{Week: week.String(), Thresholds: th, Now: now,
		BillingRows: map[string][]attribution.CostRow{}}
	sources := []attribution.SourceStatus{}

	// --- stored VM CPU (metric snapshots) ---
	series := map[string]*attribution.VMCPUSeries{}
	var order []string
	var missingMetrics []isoweek.Week
	for _, wk := range weeks {
		_, rows, err := store.LatestMetricSnapshot(ctx, d.Store.DB, wk)
		if errors.Is(err, store.ErrNotFound) {
			missingMetrics = append(missingMetrics, wk)
			continue
		}
		if err != nil {
			return ErrInternal(err)
		}
		for _, row := range rows {
			if row.Group != gcpsource.GroupVM || row.Resource == "" || (scope != store.ScopeAll && row.Project != scope) {
				continue
			}
			k := row.Project + "\x00" + row.Resource
			s := series[k]
			if s == nil {
				s = &attribution.VMCPUSeries{ProjectID: row.Project, Instance: row.Resource}
				series[k] = s
				order = append(order, k)
			}
			var val *float64
			if c, ok := row.Cells[gcpsource.MCPU]; ok && c.Gap == "" {
				val = c.Value
			}
			s.Points = append(s.Points, attribution.EvidencePoint{Week: wk.String(), Value: val, Unit: "%"})
		}
	}
	in.VMCPU = make([]attribution.VMCPUSeries, 0, len(order))
	for _, k := range order {
		in.VMCPU = append(in.VMCPU, *series[k])
	}
	switch {
	case len(missingMetrics) == len(weeks):
		sources = append(sources, sourceWith(srcMonitoringVM, "skipped", "no_metric_snapshot",
			"No stored Metrics snapshot for any window week ("+weekNames(weeks)+"); low-CPU VM hints need them.",
			"Run a refresh for "+weekNames(weeks)+" (POST /api/v1/refresh) so the VM CPU metrics are stored."))
	case len(missingMetrics) > 0:
		sources = append(sources, sourceWith(srcMonitoringVM, "ok", "",
			"No stored Metrics snapshot for "+weekNames(missingMetrics)+"; VMs cannot qualify as low-CPU until those weeks exist.",
			"Run a refresh for "+weekNames(missingMetrics)+" (POST /api/v1/refresh)."))
	default:
		sources = append(sources, okSource(srcMonitoringVM))
	}

	// --- stored billing rows ---
	latest, err := store.LatestPerWeek(ctx, d.Store.DB, weeks[0], week)
	if err != nil {
		return ErrInternal(err)
	}
	ids := []int64{}
	idWeek := map[int64]isoweek.Week{}
	var missingBilling []isoweek.Week
	for _, wk := range weeks {
		snap, ok := latest[wk]
		if !ok {
			missingBilling = append(missingBilling, wk)
			continue
		}
		ids = append(ids, snap.ID)
		idWeek[snap.ID] = wk
	}
	if len(ids) > 0 {
		byID, err := store.RowsForSnapshots(ctx, d.Store.DB, ids, scope)
		if err != nil {
			return ErrInternal(err)
		}
		for _, id := range ids {
			rows := make([]attribution.CostRow, 0, len(byID[id]))
			for _, sr := range byID[id] {
				rows = append(rows, rsCostRow(sr))
			}
			in.BillingRows[idWeek[id].String()] = rows
		}
	}
	switch {
	case len(missingBilling) == len(weeks):
		sources = append(sources, sourceWith(srcBillingSnapshots, "skipped", "no_snapshot",
			"No stored billing snapshot for any window week ("+weekNames(weeks)+"); savings cannot be estimated.",
			"Run a refresh for "+weekNames(weeks)+" (POST /api/v1/refresh)."))
	case len(missingBilling) > 0:
		sources = append(sources, sourceWith(srcBillingSnapshots, "ok", "",
			"No stored billing snapshot for "+weekNames(missingBilling)+".",
			"Run a refresh for "+weekNames(missingBilling)+" (POST /api/v1/refresh)."))
	}

	// --- Compute inventory ---
	if d.Attribution.Inventory == nil {
		sources = append(sources, sourceWith(srcComputeInventory, "skipped", "not_configured",
			"The Compute inventory source is not configured; idle disk, unused IP and stale snapshot hints are skipped.",
			"Grant roles/compute.viewer to the service account and enable compute.googleapis.com."))
	} else {
		projects := d.Config.Projects
		if scope != store.ScopeAll {
			projects = []string{scope}
		}
		inv, st := d.Attribution.Inventory.Inventory(ctx, projects)
		if st.Source == "" {
			st.Source = srcComputeInventory
		}
		if st.Status == "" {
			st.Status = "ok"
		}
		sources = append(sources, st)
		if st.Status == "ok" {
			in.Inventory = &inv
		}
	}

	det := d.Attribution.Detector
	if det == nil {
		det = attribution.NewDetector()
	}
	cands := det.Detect(in)
	if cands == nil {
		cands = []attribution.Candidate{}
	}
	total := 0.0
	for _, c := range cands {
		if c.EstimatedMonthlySaving != nil {
			total += *c.EstimatedMonthlySaving
		}
	}
	writeJSON(w, http.StatusOK, attribution.RightsizingResponse{
		Week: week.String(), Scope: scope, Currency: d.Config.Currency, GeneratedAt: now.Truncate(time.Second),
		MonthlyFactor: math.Round(attribution.MonthlyFactor*1e6) / 1e6,
		Thresholds:    th, Sources: sources, Candidates: cands,
		Totals: attribution.RightsizingTotals{Count: len(cands), EstimatedMonthlySaving: math.Round(total*100) / 100},
	})
	return nil
}

func rsCostRow(sr store.SnapshotRow) attribution.CostRow {
	row := attribution.Row{ProjectID: sr.ProjectID, Service: sr.Service, SKU: sr.SKU}
	if sr.ResourceName != nil {
		row.ResourceName = *sr.ResourceName
	}
	if sr.ResourceGlobalName != nil {
		row.ResourceGlobalName = *sr.ResourceGlobalName
	}
	return attribution.CostRow{Row: row, Gross: sr.Gross, Credits: sr.Credits, Net: sr.Net, LineItems: sr.LineItems}
}
