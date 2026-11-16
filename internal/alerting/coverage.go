package alerting

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// VMInfo is one Compute Engine instance.
type VMInfo struct {
	Project string
	Name    string
	ID      string // numeric instance id (string)
}

// DBInfo is one Cloud SQL instance.
type DBInfo struct {
	Project string
	Name    string
	Tier    string // e.g. db-f1-micro (shared-core), db-custom-1-3840
}

// ServiceInfo is one Cloud Run service.
type ServiceInfo struct {
	Project string
	Name    string
}

// CoverageSource supplies the live inventory and policies for coverage.
type CoverageSource interface {
	VMs(ctx context.Context) ([]VMInfo, error)
	CloudSQL(ctx context.Context) ([]DBInfo, error)
	CloudRun(ctx context.Context) ([]ServiceInfo, error)
	Policies(ctx context.Context) ([]PolicyView, error)
	HasOpsAgent(ctx context.Context, vm VMInfo) (bool, error)
}

type coverageProvider struct{ src CoverageSource }

// NewCoverageProvider returns the CoverageProvider over src.
func NewCoverageProvider(src CoverageSource) CoverageProvider { return &coverageProvider{src: src} }

// Metric types that make a policy cover a signal.
const (
	metricVMCPU       = "compute.googleapis.com/instance/cpu/utilization"
	metricVMMemory    = "agent.googleapis.com/memory/"
	metricVMDisk      = "agent.googleapis.com/disk/"
	metricSQLUp       = "cloudsql.googleapis.com/database/up"
	metricSQLCPU      = "cloudsql.googleapis.com/database/cpu/utilization"
	metricSQLMemory   = "cloudsql.googleapis.com/database/memory/"
	metricSQLDisk     = "cloudsql.googleapis.com/database/disk/"
	metricRunRequests = "run.googleapis.com/request_count"
)

type condText struct {
	filter string // lower-cased filter (+ raw JSON, which carries denominatorFilter)
	log    bool
}

type policyIndex struct {
	p     PolicyView
	conds []condText
}

func indexPolicies(ps []PolicyView) []policyIndex {
	var out []policyIndex
	for _, p := range ps {
		if !p.Enabled {
			continue
		}
		pi := policyIndex{p: p}
		for _, c := range p.Conditions {
			pi.conds = append(pi.conds, condText{filter: strings.ToLower(c.Filter), log: c.Type == "log_match"})
		}
		if len(p.RawJSON) > 0 {
			pi.conds = append(pi.conds, condText{filter: strings.ToLower(string(p.RawJSON))})
		}
		out = append(out, pi)
	}
	return out
}

// restrictors are filter fragments that narrow a policy to named resources.
var restrictors = []string{"instance_name", "instance_id", "system_labels.name", "service_name", "database_id", "metadata.user_labels"}

func narrowed(filter string) bool {
	for _, r := range restrictors {
		if strings.Contains(filter, r) {
			return true
		}
	}
	return false
}

func isIdent(c byte) bool {
	return c == '-' || c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z'
}

// containsToken reports whether tok occurs in s delimited by non-identifier characters.
func containsToken(s, tok string) bool {
	if tok == "" {
		return false
	}
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], tok)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(tok)
		if (a == 0 || !isIdent(s[a-1])) && (b >= len(s) || !isIdent(s[b])) {
			return true
		}
		i = b
	}
	return false
}

func mentionsAny(filter string, names []string) bool {
	for _, n := range names {
		if containsToken(filter, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// covers: some condition mentions metric and, when it is narrowed to named
// resources, names one of them.
func (pi policyIndex) covers(metric string, names ...string) bool {
	for _, c := range pi.conds {
		if !strings.Contains(c.filter, metric) {
			continue
		}
		if !narrowed(c.filter) || mentionsAny(c.filter, names) {
			return true
		}
	}
	return false
}

// coversRunErrors: a 5xx request_count condition or an error log-match on Cloud Run.
func (pi policyIndex) coversRunErrors(service string) bool {
	for _, c := range pi.conds {
		isMetric := strings.Contains(c.filter, metricRunRequests) && strings.Contains(c.filter, "5xx")
		isLog := c.log && strings.Contains(c.filter, "cloud_run_revision") && strings.Contains(c.filter, "severity")
		if !isMetric && !isLog {
			continue
		}
		if !narrowed(c.filter) || mentionsAny(c.filter, []string{service}) {
			return true
		}
	}
	return false
}

// policiesFor returns the names of enabled policies of project for which match is true.
func policiesFor(idx []policyIndex, project string, match func(policyIndex) bool) []string {
	out := []string{}
	for _, pi := range idx {
		if pi.p.Project == project && match(pi) {
			out = append(out, pi.p.Name)
		}
	}
	sort.Strings(out)
	return out
}

func signal(name string, pols []string, reason string) CoverageSignal {
	if len(pols) > 0 {
		return CoverageSignal{Signal: name, Covered: true, Policies: pols}
	}
	return CoverageSignal{Signal: name, Policies: []string{}, Reason: reason}
}

const reasonNoPolicy = "No enabled policy covers this signal"

// Coverage implements CoverageProvider.
func (c *coverageProvider) Coverage(ctx context.Context) ([]CoverageRow, error) {
	pols, err := c.src.Policies(ctx)
	if err != nil {
		return nil, fmt.Errorf("coverage: policies: %w", err)
	}
	vms, err := c.src.VMs(ctx)
	if err != nil {
		return nil, fmt.Errorf("coverage: vms: %w", err)
	}
	dbs, err := c.src.CloudSQL(ctx)
	if err != nil {
		return nil, fmt.Errorf("coverage: cloud sql: %w", err)
	}
	runs, err := c.src.CloudRun(ctx)
	if err != nil {
		return nil, fmt.Errorf("coverage: cloud run: %w", err)
	}
	idx := indexPolicies(pols)
	rows := []CoverageRow{}
	for _, vm := range vms {
		rows = append(rows, c.vmRow(ctx, idx, vm))
	}
	for _, db := range dbs {
		rows = append(rows, sqlRow(idx, db))
	}
	for _, s := range runs {
		rows = append(rows, runRow(idx, s))
	}
	order := map[string]int{"vm": 0, "cloudsql": 1, "cloud_run": 2}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.ResourceType != b.ResourceType {
			return order[a.ResourceType] < order[b.ResourceType]
		}
		return a.Resource < b.Resource
	})
	return rows, nil
}

func (c *coverageProvider) vmRow(ctx context.Context, idx []policyIndex, vm VMInfo) CoverageRow {
	names := []string{vm.Name, vm.ID}
	agent, aerr := c.src.HasOpsAgent(ctx, vm)
	agentReason := "Ops Agent not installed: memory/disk metrics unavailable"
	if aerr != nil {
		agent = false
		agentReason = "Ops Agent presence could not be checked"
	}
	cpu := signal("cpu", policiesFor(idx, vm.Project, func(p policyIndex) bool { return p.covers(metricVMCPU, names...) }), reasonNoPolicy)
	mem, disk := CoverageSignal{Signal: "memory", Policies: []string{}, Reason: agentReason}, CoverageSignal{Signal: "disk", Policies: []string{}, Reason: agentReason}
	if agent {
		mem = signal("memory", policiesFor(idx, vm.Project, func(p policyIndex) bool { return p.covers(metricVMMemory, names...) }), reasonNoPolicy)
		disk = signal("disk", policiesFor(idx, vm.Project, func(p policyIndex) bool { return p.covers(metricVMDisk, names...) }), reasonNoPolicy)
	}
	row := CoverageRow{Project: vm.Project, ResourceType: "vm", Resource: vm.Name, Signals: []CoverageSignal{cpu, mem, disk}}
	var parts []string
	switch {
	case !cpu.Covered && !agent:
		parts = append(parts, fmt.Sprintf("VM %s has no CPU alert: apply the VM CPU template. Install the Ops Agent to alert on memory/disk.", vm.Name))
	case !cpu.Covered:
		parts = append(parts, fmt.Sprintf("VM %s has no CPU alert: apply the VM CPU template.", vm.Name))
	case !agent:
		parts = append(parts, fmt.Sprintf("VM %s has CPU alert but no memory/disk alert coverage: install the Ops Agent.", vm.Name))
	}
	if agent && (!mem.Covered || !disk.Covered) {
		var miss []string
		if !mem.Covered {
			miss = append(miss, "memory")
		}
		if !disk.Covered {
			miss = append(miss, "disk")
		}
		parts = append(parts, fmt.Sprintf("VM %s has the Ops Agent but no %s alert: apply the VM %s template.", vm.Name, strings.Join(miss, "/"), strings.Join(miss, "/")))
	}
	row.Hint = strings.Join(parts, " ")
	return row
}

func sharedCore(tier string) bool {
	t := strings.ToLower(tier)
	return t == "db-f1-micro" || t == "db-g1-small"
}

func sqlRow(idx []policyIndex, db DBInfo) CoverageRow {
	names := []string{db.Name, db.Project + ":" + db.Name}
	mk := func(sig, metric, reason string) CoverageSignal {
		return signal(sig, policiesFor(idx, db.Project, func(p policyIndex) bool { return p.covers(metric, names...) }), reason)
	}
	memReason := reasonNoPolicy
	if sharedCore(db.Tier) {
		memReason = "Shared-core tier: the memory utilization metric reads 100 %; use total_usage ÷ quota"
	}
	down := mk("down", metricSQLUp, reasonNoPolicy)
	cpu := mk("cpu", metricSQLCPU, reasonNoPolicy)
	mem := mk("memory", metricSQLMemory, memReason)
	disk := mk("disk", metricSQLDisk, reasonNoPolicy)
	row := CoverageRow{Project: db.Project, ResourceType: "cloudsql", Resource: db.Name, Signals: []CoverageSignal{down, cpu, mem, disk}}
	var miss []string
	for _, s := range row.Signals {
		if !s.Covered {
			miss = append(miss, s.Signal)
		}
	}
	switch {
	case !down.Covered && len(miss) > 1:
		row.Hint = fmt.Sprintf("Cloud SQL %s has no database-down alert (also missing: %s): apply the Cloud SQL templates.", db.Name, strings.Join(miss[1:], ", "))
	case !down.Covered:
		row.Hint = fmt.Sprintf("Cloud SQL %s has no database-down alert: apply the Cloud SQL down template.", db.Name)
	case len(miss) > 0:
		row.Hint = fmt.Sprintf("Cloud SQL %s has no %s alert: apply the matching Cloud SQL template.", db.Name, strings.Join(miss, "/"))
	}
	return row
}

func runRow(idx []policyIndex, s ServiceInfo) CoverageRow {
	sig := signal("errors", policiesFor(idx, s.Project, func(p policyIndex) bool { return p.coversRunErrors(s.Name) }), reasonNoPolicy)
	row := CoverageRow{Project: s.Project, ResourceType: "cloud_run", Resource: s.Name, Signals: []CoverageSignal{sig}}
	if !sig.Covered {
		row.Hint = fmt.Sprintf("Cloud Run service %s has no 5xx/error alert: apply the Cloud Run 5xx templates.", s.Name)
	}
	return row
}
