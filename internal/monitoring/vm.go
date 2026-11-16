package monitoring

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// SourceVM is the run_sources id of the VM family.
const SourceVM = "monitoring_vm"

// Metric filters used by FetchVM.
const (
	vmCPUFilter                               = `metric.type="compute.googleapis.com/instance/cpu/utilization" AND resource.type="gce_instance"`
	vmMemFilter                               = `metric.type="agent.googleapis.com/memory/percent_used" AND resource.type="gce_instance" AND metric.labels.state="used"`
	vmDiskPctFilter                           = `metric.type="agent.googleapis.com/disk/percent_used" AND resource.type="gce_instance" AND metric.labels.state="used"`
	vmDiskBytesFilter                         = `metric.type="agent.googleapis.com/disk/bytes_used" AND resource.type="gce_instance"`
	vmHintNoAgent                             = "install the Ops Agent on this VM to collect memory and disk"
	vmHintNoAgentSeries                       = "the Ops Agent reported no series for this metric in the window; check the agent's metrics pipeline on this VM"
	vmGapNoAgent                              = "no agent"
	vmGapNoData                               = "no data"
	vmGapSourceFailed                         = "source failed"
	vmUnitPct                                 = "%"
	vmLabelInstanceID                         = "instance_id"
	vmLabelInstanceName                       = "instance_name"
	vmLabelDevice                             = "device"
	vmLabelState                              = "state"
	vmResourceInstanceID                      = "resource.labels.instance_id"
	vmMetricInstanceName                      = "metric.labels.instance_name"
	vmStateUsed, vmStateFree, vmStateReserved = "used", "free", "reserved"
)

// vmRootDevices are the boot-disk root partitions of the GCE public images
// (Debian/Ubuntu/RHEL on SCSI → /dev/sda1, on NVMe → /dev/nvme0n1p1).
// The Ops Agent disk metrics carry only a device label (no mountpoint), so
// the root filesystem is identified by device: the first of these that the
// VM reports wins; otherwise the largest non-loop device (snap /dev/loop*
// squashfs mounts, the EFI partition and /boot are always smaller).
var vmRootDevices = []string{"/dev/sda1", "/dev/nvme0n1p1", "/dev/root"}

// FetchVM returns one MetricRow per VM (GroupVM) with weekly-mean CPU,
// memory and root-disk utilisation in percent, across every project.
//
// CPU comes from compute.googleapis.com (all VMs). Memory and disk come from
// the Ops Agent (agent.googleapis.com), which identifies a VM only by
// resource.labels.instance_id; the CPU query is grouped by both instance
// name and id so agent series map back to names. Disk uses
// agent disk/percent_used (state=used) when the project reports it, else it
// is derived from disk/bytes_used as used / (used+free+reserved) × 100 —
// the same definition percent_used has. A VM with CPU but no agent series
// gets a "no agent" gap, never 0. A project whose call fails contributes no
// rows and is listed in Message; only when every project fails is the
// result classified as a failure.
func FetchVM(ctx context.Context, l Lister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	res := gcpsource.Result{Source: SourceVM, Started: now()}
	var failures []string
	var firstErr error
	okProjects := 0
	for _, raw := range projects {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		rows, provs, err := fetchVMProject(ctx, l, p, w, now)
		res.Provenance = append(res.Provenance, provs...)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			st, msg, _ := gcpsource.Classify(err, RoleHint, API)
			failures = append(failures, fmt.Sprintf("%s: %s (%s)", p, st, msg))
			continue
		}
		okProjects++
		res.Metrics = append(res.Metrics, rows...)
	}
	if okProjects == 0 && firstErr != nil {
		res.Finish(now(), firstErr, RoleHint, API)
		if len(failures) > 1 {
			res.Message = strings.Join(failures, "; ")
		}
		return res
	}
	if len(failures) > 0 {
		res.Message = "projects skipped: " + strings.Join(failures, "; ")
	}
	sort.SliceStable(res.Metrics, func(i, j int) bool {
		a, b := res.Metrics[i], res.Metrics[j]
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Resource < b.Resource
	})
	res.Finish(now(), nil, RoleHint, API)
	return res
}

type vmKey struct{ id, name string }

// fetchVMProject runs the queries for one project. A CPU failure fails the
// project; an agent-query failure only turns that column into "source failed".
func fetchVMProject(ctx context.Context, l Lister, project string, w gcpsource.Window, now func() time.Time) ([]gcpsource.MetricRow, []gcpsource.Provenance, error) {
	var provs []gcpsource.Provenance

	cpuQ := Query{
		Project: project, Filter: vmCPUFilter, Window: w,
		Aligner: monitoringpb.Aggregation_ALIGN_MEAN,
		Reducer: monitoringpb.Aggregation_REDUCE_MEAN,
		GroupBy: []string{vmMetricInstanceName, vmResourceInstanceID},
	}
	cpuTS, err := cpuQ.Run(ctx, l)
	cpuProv := cpuQ.Provenance(SourceVM, now())
	provs = append(provs, cpuProv)
	if err != nil {
		return nil, provs, fmt.Errorf("monitoring: vm cpu %s: %w", project, err)
	}

	// name → mean CPU fraction; id → name.
	cpu := map[string]float64{}
	nameByID := map[string]string{}
	var vms []vmKey
	for _, ts := range cpuTS {
		name := Label(ts, vmLabelInstanceName)
		if name == "" || len(ts.GetPoints()) == 0 {
			continue
		}
		id := Label(ts, vmLabelInstanceID)
		if _, seen := cpu[name]; !seen {
			vms = append(vms, vmKey{id: id, name: name})
		}
		cpu[name] = vmMean(ts)
		if id != "" {
			nameByID[id] = name
		}
	}
	if len(vms) == 0 {
		return nil, provs, nil
	}

	memQ := Query{Project: project, Filter: vmMemFilter, Window: w, Aligner: monitoringpb.Aggregation_ALIGN_MEAN}
	memTS, memErr := memQ.Run(ctx, l)
	memProv := memQ.Provenance(SourceVM, now())
	provs = append(provs, memProv)
	mem := map[string]float64{}
	if memErr == nil {
		for _, ts := range memTS {
			if name, ok := nameByID[Label(ts, vmLabelInstanceID)]; ok && len(ts.GetPoints()) > 0 {
				mem[name] = vmMean(ts)
			}
		}
	}

	disk, diskProv, diskErr := vmFetchDisk(ctx, l, project, w, nameByID, now, &provs)

	rows := make([]gcpsource.MetricRow, 0, len(vms))
	for _, vm := range vms {
		cells := map[string]gcpsource.Cell{
			gcpsource.MCPU: gcpsource.V(cpu[vm.name]*100, vmUnitPct, cpuProv),
		}
		_, hasMem := mem[vm.name]
		_, hasDisk := disk[vm.name]
		hasAgent := hasMem || hasDisk
		cells[gcpsource.MMemory] = vmAgentCell(mem, vm.name, memErr, hasAgent, memProv)
		cells[gcpsource.MDisk] = vmAgentCell(disk, vm.name, diskErr, hasAgent, diskProv)
		rows = append(rows, gcpsource.MetricRow{
			Group: gcpsource.GroupVM, Project: project, Resource: vm.name, Cells: cells,
		})
	}
	return rows, provs, nil
}

// vmAgentCell renders one Ops Agent column for a VM.
func vmAgentCell(vals map[string]float64, name string, qerr error, hasAgent bool, prov gcpsource.Provenance) gcpsource.Cell {
	if qerr != nil {
		_, _, hint := gcpsource.Classify(qerr, RoleHint, API)
		return gcpsource.G(vmGapSourceFailed, hint, vmUnitPct, prov)
	}
	if v, ok := vals[name]; ok {
		return gcpsource.V(v, vmUnitPct, prov)
	}
	if hasAgent {
		return gcpsource.G(vmGapNoData, vmHintNoAgentSeries, vmUnitPct, prov)
	}
	return gcpsource.G(vmGapNoAgent, vmHintNoAgent, vmUnitPct, prov)
}

// vmFetchDisk returns root-disk percent used per VM name and the provenance of
// the query that produced the values (percent_used, or bytes_used fallback).
func vmFetchDisk(ctx context.Context, l Lister, project string, w gcpsource.Window, nameByID map[string]string, now func() time.Time, provs *[]gcpsource.Provenance) (map[string]float64, gcpsource.Provenance, error) {
	pctQ := Query{Project: project, Filter: vmDiskPctFilter, Window: w, Aligner: monitoringpb.Aggregation_ALIGN_MEAN}
	pctTS, err := pctQ.Run(ctx, l)
	pctProv := pctQ.Provenance(SourceVM, now())
	*provs = append(*provs, pctProv)
	if err != nil {
		return nil, pctProv, err
	}
	if len(pctTS) > 0 {
		// device → percent per VM; total size is unknown here, so the root is
		// chosen by name only (falls back to the highest non-loop device).
		byVM := map[string]map[string]float64{}
		for _, ts := range pctTS {
			name, ok := nameByID[Label(ts, vmLabelInstanceID)]
			if !ok || len(ts.GetPoints()) == 0 {
				continue
			}
			if byVM[name] == nil {
				byVM[name] = map[string]float64{}
			}
			byVM[name][Label(ts, vmLabelDevice)] = vmMean(ts)
		}
		out := map[string]float64{}
		for name, devs := range byVM {
			if dev, ok := vmPickRoot(devs); ok {
				out[name] = devs[dev]
			}
		}
		return out, pctProv, nil
	}

	bytesQ := Query{Project: project, Filter: vmDiskBytesFilter, Window: w, Aligner: monitoringpb.Aggregation_ALIGN_MEAN}
	bytesTS, err := bytesQ.Run(ctx, l)
	bytesProv := bytesQ.Provenance(SourceVM, now())
	*provs = append(*provs, bytesProv)
	if err != nil {
		return nil, bytesProv, err
	}
	type vmUsage struct{ used, total float64 }
	byVM := map[string]map[string]*vmUsage{}
	for _, ts := range bytesTS {
		name, ok := nameByID[Label(ts, vmLabelInstanceID)]
		if !ok || len(ts.GetPoints()) == 0 {
			continue
		}
		dev := Label(ts, vmLabelDevice)
		if byVM[name] == nil {
			byVM[name] = map[string]*vmUsage{}
		}
		u := byVM[name][dev]
		if u == nil {
			u = &vmUsage{}
			byVM[name][dev] = u
		}
		v := vmMean(ts)
		switch Label(ts, vmLabelState) {
		case vmStateUsed:
			u.used += v
			u.total += v
		case vmStateFree, vmStateReserved:
			u.total += v
		}
	}
	out := map[string]float64{}
	for name, devs := range byVM {
		sizes := map[string]float64{}
		for dev, u := range devs {
			if u.total > 0 {
				sizes[dev] = u.total
			}
		}
		if dev, ok := vmPickRoot(sizes); ok {
			u := devs[dev]
			out[name] = u.used / u.total * 100
		}
	}
	return out, bytesProv, nil
}

// vmPickRoot selects the root device from device → weight (size in bytes, or
// any comparable value): a known root partition name first, else the
// largest non-loop device.
func vmPickRoot(devs map[string]float64) (string, bool) {
	for _, d := range vmRootDevices {
		if _, ok := devs[d]; ok {
			return d, true
		}
	}
	best, bestV, found := "", 0.0, false
	for d, v := range devs {
		if d == "" || strings.HasPrefix(d, "/dev/loop") {
			continue
		}
		if !found || v > bestV || (v == bestV && d < best) {
			best, bestV, found = d, v, true
		}
	}
	return best, found
}

// vmMean is the mean of a series' point values (one point when the
// alignment period equals the window; robust if the API splits it).
func vmMean(ts *monitoringpb.TimeSeries) float64 {
	n := len(ts.GetPoints())
	if n == 0 {
		return 0
	}
	return Value(ts) / float64(n)
}
