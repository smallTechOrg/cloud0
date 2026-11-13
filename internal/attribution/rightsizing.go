package attribution

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// TypeComputeSnapshot is the resource type of stale-snapshot candidates.
const TypeComputeSnapshot = "compute_snapshot"

const rsNoAction = " cloud0 makes no changes."

var rsCoreRAM = regexp.MustCompile(`(?i)\b(core|ram)\b`)

type rsDetector struct{}

// NewDetector returns the pure rightsizing Detector (spec/api.md → rightsizing).
func NewDetector() Detector { return rsDetector{} }

func rsRound2(v float64) float64 { return math.Round(v*100) / 100 }

func rsPtr(v float64) *float64 { return &v }

func rsLastSegment(s string) string {
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// rsResourceKinds are the path segments that identify a Compute resource kind in
// a billing resource name (projects/<n>/<kind>/<name>).
var rsResourceKinds = []string{"instances", "disks", "addresses", "snapshots"}

// rsNameMatches reports whether a billing resource name denotes the resource
// `name` of the given kind: last path segment equal, and the path does not
// identify a different kind (a boot disk shares its VM's name).
func rsNameMatches(resource, kind, name string) bool {
	if resource == "" || name == "" || rsLastSegment(resource) != name {
		return false
	}
	lower := strings.ToLower(resource)
	if strings.Contains(lower, "/"+kind+"/") {
		return true
	}
	for _, k := range rsResourceKinds {
		if k != kind && strings.Contains(lower, "/"+k+"/") {
			return false
		}
	}
	return true
}

func rsIsGCE(service string) bool {
	return strings.Contains(strings.ToLower(service), "compute engine")
}

// rsNetFor sums the net of the week's rows for which pick returns true; ok is
// false when no row matched.
func rsNetFor(rows []CostRow, pick func(CostRow) bool) (net float64, ok bool) {
	for _, r := range rows {
		if pick(r) {
			net += r.Net
			ok = true
		}
	}
	return net, ok
}

type rsWindow struct {
	weeks []string
	start string
	end   string
}

func rsWindowOf(in RightsizingInput) (rsWindow, bool) {
	last, err := isoweek.Parse(in.Week)
	if err != nil {
		return rsWindow{}, false
	}
	n := in.Thresholds.Weeks
	if n < 1 {
		n = DefaultWeeks
	}
	ws := make([]isoweek.Week, n)
	cur := last
	for i := n - 1; i >= 0; i-- {
		ws[i] = cur
		cur = cur.Prev()
	}
	w := rsWindow{start: ws[0].Start().Format("2006-01-02"), end: last.Sunday().Format("2006-01-02")}
	for _, x := range ws {
		w.weeks = append(w.weeks, x.String())
	}
	return w, true
}

func rsKey(project, name string) string { return project + "\x00" + name }

// Detect implements Detector.
func (rsDetector) Detect(in RightsizingInput) []Candidate {
	out := []Candidate{}
	win, ok := rsWindowOf(in)
	if !ok {
		return out
	}
	th := in.Thresholds
	if th.CPUPct <= 0 {
		th.CPUPct = DefaultCPUPct
	}
	if th.StaleSnapshotDays <= 0 {
		th.StaleSnapshotDays = DefaultStaleSnapshotDays
	}
	if th.Weeks < 1 {
		th.Weeks = DefaultWeeks
	}
	in.Thresholds = th
	lastRows := in.BillingRows[in.Week]

	out = append(out, detectLowCPU(in, win, lastRows)...)
	out = append(out, detectCloudRun(in, win)...)
	if in.Inventory != nil {
		out = append(out, detectInventory(in, lastRows)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].EstimatedMonthlySaving, out[j].EstimatedMonthlySaving
		switch {
		case a != nil && b != nil && *a != *b:
			return *a > *b
		case a != nil && b == nil:
			return true
		case a == nil && b != nil:
			return false
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func detectLowCPU(in RightsizingInput, win rsWindow, lastRows []CostRow) []Candidate {
	var out []Candidate
	for _, s := range in.VMCPU {
		byWeek := map[string]*float64{}
		for _, p := range s.Points {
			if p.Value != nil {
				byWeek[p.Week] = p.Value
			}
		}
		points := make([]EvidencePoint, 0, len(win.weeks))
		low := true
		for _, w := range win.weeks {
			v := byWeek[w]
			points = append(points, EvidencePoint{Week: w, Value: v, Unit: "%"})
			if v == nil || math.IsNaN(*v) || !(*v < in.Thresholds.CPUPct) {
				low = false
			}
		}
		if !low {
			continue
		}
		c := Candidate{
			ID: fmt.Sprintf("%s:%s:%s", KindLowCPUVM, s.ProjectID, s.Instance), Kind: KindLowCPUVM,
			ProjectID: s.ProjectID, ResourceName: s.Instance, ResourceType: TypeComputeInstance,
			Confidence: ConfidenceMedium,
			Evidence: Evidence{WindowStart: win.start, WindowEnd: win.end, Points: points,
				Notes: []string{fmt.Sprintf("mean CPU < %s %% in each of the last %d stored weeks", rsTrim(in.Thresholds.CPUPct), in.Thresholds.Weeks)}},
			Action: "Review the workload; consider a smaller machine type or stopping the instance." + rsNoAction,
		}
		if s.Zone != "" {
			z := s.Zone
			c.Location = &z
		}
		net, found := rsNetFor(lastRows, func(r CostRow) bool {
			return r.ProjectID == s.ProjectID && rsIsGCE(r.Service) &&
				rsNameMatches(r.ResourceName, "instances", s.Instance) && rsCoreRAM.MatchString(r.SKU)
		})
		if found {
			net = rsRound2(net)
			c.NetLastWeek = rsPtr(net)
			c.EstimatedMonthlySaving = rsPtr(rsRound2(net * VMDownsizeSavingFraction * MonthlyFactor))
			c.SavingBasis = fmt.Sprintf("50 %% of last week's instance core+RAM net (%.2f) × %.3f weeks/month — assumes one machine size down", net*VMDownsizeSavingFraction, MonthlyFactor)
		} else {
			c.SavingBasis = "No Compute Engine core/RAM billing row matched this VM in " + in.Week + "; saving cannot be estimated."
		}
		out = append(out, c)
	}
	return out
}

func rsTrim(v float64) string { return fmt.Sprintf("%g", v) }

func rsIsIdleSKU(sku string) bool {
	l := strings.ToLower(sku)
	return strings.Contains(l, "idle") || strings.Contains(l, "min instance")
}

func rsIsCloudRun(service string) bool {
	return strings.Contains(strings.ToLower(service), "cloud run")
}

func detectCloudRun(in RightsizingInput, win rsWindow) []Candidate {
	type agg struct{ idle, total float64 }
	perWeek := func(rows []CostRow) map[string]*agg {
		m := map[string]*agg{}
		for _, r := range rows {
			if !rsIsCloudRun(r.Service) || r.ResourceName == "" {
				continue
			}
			k := rsKey(r.ProjectID, rsLastSegment(r.ResourceName))
			a := m[k]
			if a == nil {
				a = &agg{}
				m[k] = a
			}
			a.total += r.Net
			if rsIsIdleSKU(r.SKU) {
				a.idle += r.Net
			}
		}
		return m
	}
	lastRows, have := in.BillingRows[in.Week]
	if !have {
		return nil
	}
	weekly := map[string]map[string]*agg{}
	for _, w := range win.weeks {
		if rows, ok := in.BillingRows[w]; ok {
			weekly[w] = perWeek(rows)
		}
	}
	cur := perWeek(lastRows)
	keys := make([]string, 0, len(cur))
	for k := range cur {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Candidate
	for _, k := range keys {
		a := cur[k]
		if a.idle <= 0 || a.idle < in.Thresholds.IdleRunMinNet {
			continue
		}
		parts := strings.SplitN(k, "\x00", 2)
		project, svc := parts[0], parts[1]
		var points []EvidencePoint
		for _, w := range win.weeks {
			m, ok := weekly[w]
			if !ok {
				continue
			}
			v := 0.0
			if x := m[k]; x != nil {
				v = rsRound2(x.idle)
			}
			points = append(points, EvidencePoint{Week: w, Value: rsPtr(v), Unit: moneyUnit})
		}
		if points == nil {
			points = []EvidencePoint{}
		}
		idle := rsRound2(a.idle)
		active := rsRound2(a.total - a.idle)
		note := fmt.Sprintf("idle (min-instance) net %.2f vs %.2f for the service's other SKUs in %s", idle, active, in.Week)
		if a.idle > a.total-a.idle {
			note += " — idle cost dominates"
		}
		out = append(out, Candidate{
			ID: fmt.Sprintf("%s:%s:%s", KindCloudRunMinInst, project, svc), Kind: KindCloudRunMinInst,
			ProjectID: project, ResourceName: svc, ResourceType: TypeCloudRunService, Confidence: ConfidenceMedium,
			Evidence:               Evidence{WindowStart: win.start, WindowEnd: win.end, Points: points, Notes: []string{note}},
			NetLastWeek:            rsPtr(idle),
			EstimatedMonthlySaving: rsPtr(rsRound2(a.idle * MonthlyFactor)),
			SavingBasis:            fmt.Sprintf("last week's min-instance idle net (%.2f) × %.3f weeks/month — what lowering minScale to 0 would remove if traffic tolerates cold starts", idle, MonthlyFactor),
			Action:                 "Consider setting minScale to 0 for this service if its traffic tolerates cold starts." + rsNoAction,
		})
	}
	return out
}

// moneyUnit labels money evidence points; the detector is currency-agnostic.
const moneyUnit = "net"

func detectInventory(in RightsizingInput, lastRows []CostRow) []Candidate {
	inv := in.Inventory
	at := inv.FetchedAt
	if at.IsZero() {
		at = in.Now
	}
	day := at.UTC().Format("2006-01-02")
	stamp := at.UTC().Format(time.RFC3339)
	var out []Candidate

	mk := func(kind, project, name, rtype, loc, conf, noun, kindSeg, actionText string, notes []string) Candidate {
		c := Candidate{
			ID: fmt.Sprintf("%s:%s:%s", kind, project, name), Kind: kind, ProjectID: project, ResourceName: name,
			ResourceType: rtype, Confidence: conf,
			Evidence: Evidence{WindowStart: day, WindowEnd: day, Points: []EvidencePoint{}, Notes: notes},
			Action:   actionText + rsNoAction,
		}
		if loc != "" {
			l := loc
			c.Location = &l
		}
		net, found := rsNetFor(lastRows, func(r CostRow) bool {
			return r.ProjectID == project && rsNameMatches(r.ResourceName, kindSeg, name)
		})
		if found {
			net = rsRound2(net)
			c.NetLastWeek = rsPtr(net)
			c.EstimatedMonthlySaving = rsPtr(rsRound2(net * MonthlyFactor))
			c.SavingBasis = fmt.Sprintf("last week's %s net × %.3f weeks/month", noun, MonthlyFactor)
		} else {
			c.SavingBasis = "No billing row matched this " + noun + " by name in " + in.Week + "; saving cannot be estimated."
		}
		return c
	}

	for _, d := range inv.Disks {
		if strings.EqualFold(d.Status, "READY") && len(d.Users) == 0 {
			out = append(out, mk(KindIdleDisk, d.ProjectID, d.Name, TypeComputeDisk, d.Zone, ConfidenceHigh, "disk", "disks",
				"Snapshot the disk if its data matters, then delete it.",
				[]string{fmt.Sprintf("status READY, no attached users (Compute API at %s)", stamp)}))
		}
	}
	for _, a := range inv.Addresses {
		if strings.EqualFold(a.Status, "RESERVED") && len(a.Users) == 0 {
			out = append(out, mk(KindUnusedIP, a.ProjectID, a.Name, TypeComputeAddress, a.Region, ConfidenceHigh, "address", "addresses",
				"Release the reserved address if nothing needs it.",
				[]string{fmt.Sprintf("status RESERVED, no users (Compute API at %s)", stamp)}))
		}
	}
	diskExists := map[string]bool{}
	for _, d := range inv.Disks {
		diskExists[rsKey(d.ProjectID, d.Name)] = true
	}
	limit := time.Duration(in.Thresholds.StaleSnapshotDays) * 24 * time.Hour
	for _, s := range inv.Snapshots {
		if s.CreatedAt.IsZero() {
			continue
		}
		age := in.Now.Sub(s.CreatedAt)
		if age <= limit {
			continue
		}
		src := rsLastSegment(s.SourceDisk)
		gone := src == "" || !diskExists[rsKey(s.ProjectID, src)]
		veryOld := age > 4*limit
		if !gone && !veryOld {
			continue
		}
		days := int(age.Hours() / 24)
		reason := fmt.Sprintf("older than 4× the %d-day threshold", in.Thresholds.StaleSnapshotDays)
		if gone {
			reason = "its source disk no longer exists"
		}
		out = append(out, mk(KindStaleSnapshot, s.ProjectID, s.Name, TypeComputeSnapshot, "", ConfidenceMedium, "snapshot", "snapshots",
			"Delete the snapshot if you no longer need to restore from it.",
			[]string{fmt.Sprintf("created %s (%d days old); %s (Compute API at %s)", s.CreatedAt.UTC().Format("2006-01-02"), days, reason, stamp)}))
	}
	return out
}
