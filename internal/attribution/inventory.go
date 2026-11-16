package attribution

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	// SourceComputeInventory is the SourceStatus.Source of the Compute inventory.
	SourceComputeInventory = "compute_inventory"
	// ErrCodePermissionDenied / ErrCodeUpstream are the SourceStatus.ErrorCode values.
	ErrCodePermissionDenied = "permission_denied"
	ErrCodeUpstream         = "upstream_error"

	inventoryScope          = "https://www.googleapis.com/auth/cloud-platform"
	inventoryProjectTimeout = 20 * time.Second
	inventoryFixHint        = "Grant roles/compute.viewer to the service account and enable compute.googleapis.com."
)

// VM is a Compute Engine instance (inventory extra, lets the detector match
// billing resource names).
type VM struct {
	ProjectID   string
	Name        string
	Zone        string
	MachineType string
	Status      string
	Labels      map[string]string
}

// DiskDetail extends Disk with labels and lifecycle timestamps.
type DiskDetail struct {
	Disk
	Labels         map[string]string
	CreatedAt      time.Time
	LastAttachedAt time.Time
	LastDetachedAt time.Time
}

// AddressDetail extends Address with its scope and labels.
type AddressDetail struct {
	Address
	Global bool
	Labels map[string]string
}

// SnapshotDetail extends Snapshot with labels and stored bytes.
type SnapshotDetail struct {
	Snapshot
	Labels       map[string]string
	StorageBytes int64
}

// InventoryDetail is the full read-only view; Inventory() is derived from it.
type InventoryDetail struct {
	FetchedAt time.Time
	VMs       []VM
	Disks     []DiskDetail
	Addresses []AddressDetail
	Snapshots []SnapshotDetail
	// Statuses has one entry per project and source (source "project/kind").
	Statuses []SourceStatus
}

// DetailProvider is implemented by the provider NewInventory returns, for
// callers that want VMs, labels and per-project statuses.
type DetailProvider interface {
	InventoryDetail(ctx context.Context, projects []string) InventoryDetail
}

type computeInventory struct {
	svc     *compute.Service
	timeout time.Duration
	now     func() time.Time
}

var (
	_ InventoryProvider = (*computeInventory)(nil)
	_ DetailProvider    = (*computeInventory)(nil)
)

// NewInventory builds the read-only Compute inventory provider. Pass the
// reader credentials as options; the cloud-platform scope is added. The
// returned func releases resources (nothing today; safe to call).
func NewInventory(ctx context.Context, opts ...option.ClientOption) (InventoryProvider, func() error, error) {
	ro := append(append([]option.ClientOption{}, opts...), option.WithScopes(inventoryScope))
	svc, err := compute.NewService(ctx, ro...)
	if err != nil {
		return nil, nil, fmt.Errorf("attribution: compute client: %w", err)
	}
	return &computeInventory{svc: svc, timeout: inventoryProjectTimeout, now: time.Now}, func() error { return nil }, nil
}

// Inventory implements InventoryProvider.
func (c *computeInventory) Inventory(ctx context.Context, projects []string) (Inventory, SourceStatus) {
	d := c.InventoryDetail(ctx, projects)
	inv := Inventory{FetchedAt: d.FetchedAt, Disks: []Disk{}, Addresses: []Address{}, Snapshots: []Snapshot{}}
	for _, x := range d.Disks {
		inv.Disks = append(inv.Disks, x.Disk)
	}
	for _, x := range d.Addresses {
		inv.Addresses = append(inv.Addresses, x.Address)
	}
	for _, x := range d.Snapshots {
		inv.Snapshots = append(inv.Snapshots, x.Snapshot)
	}
	return inv, summarizeStatuses(d.Statuses, len(projects))
}

// summarizeStatuses folds per-project statuses: skipped without projects;
// failed only when nothing at all could be read; otherwise ok (partial
// failures are reported in the message and hint).
func summarizeStatuses(sts []SourceStatus, nProjects int) SourceStatus {
	out := SourceStatus{Source: SourceComputeInventory, Status: "ok"}
	if nProjects == 0 {
		out.Status = "skipped"
		m := "no projects configured"
		out.Message = &m
		return out
	}
	var failed, notes []string
	var first *SourceStatus
	for i := range sts {
		s := sts[i]
		switch {
		case s.Status == "failed":
			if first == nil {
				first = &sts[i]
			}
			msg := s.Source
			if s.Message != nil {
				msg += ": " + *s.Message
			}
			failed = append(failed, msg)
		case s.Message != nil:
			notes = append(notes, s.Source+": "+*s.Message)
		}
	}
	if len(failed) > 0 {
		hint := inventoryFixHint
		if first.FixHint != nil {
			hint = *first.FixHint
		}
		out.ErrorCode = first.ErrorCode
		out.FixHint = &hint
		m := strings.Join(failed, "; ")
		if len(failed) == len(sts) {
			out.Status = "failed"
		} else {
			m = "partial: " + m
		}
		out.Message = &m
		return out
	}
	if len(notes) > 0 {
		m := strings.Join(notes, "; ")
		out.Message = &m
	}
	return out
}

// InventoryDetail lists every project; a failing project or source never
// aborts the others.
func (c *computeInventory) InventoryDetail(ctx context.Context, projects []string) InventoryDetail {
	d := InventoryDetail{FetchedAt: c.now().UTC()}
	for _, p := range projects {
		pctx, cancel := context.WithTimeout(ctx, c.timeout)
		d.Statuses = append(d.Statuses,
			c.run(p, "instances", func() error { return c.listVMs(pctx, p, &d) }),
			c.run(p, "disks", func() error { return c.listDisks(pctx, p, &d) }),
			c.run(p, "addresses", func() error { return c.listAddresses(pctx, p, &d) }),
			c.run(p, "snapshots", func() error { return c.listSnapshots(pctx, p, &d) }),
		)
		cancel()
	}
	sort.SliceStable(d.VMs, func(i, j int) bool {
		return less2(d.VMs[i].ProjectID, d.VMs[i].Name, d.VMs[j].ProjectID, d.VMs[j].Name)
	})
	sort.SliceStable(d.Disks, func(i, j int) bool {
		return less2(d.Disks[i].ProjectID, d.Disks[i].Name, d.Disks[j].ProjectID, d.Disks[j].Name)
	})
	sort.SliceStable(d.Addresses, func(i, j int) bool {
		return less2(d.Addresses[i].ProjectID, d.Addresses[i].Name, d.Addresses[j].ProjectID, d.Addresses[j].Name)
	})
	sort.SliceStable(d.Snapshots, func(i, j int) bool {
		return less2(d.Snapshots[i].ProjectID, d.Snapshots[i].Name, d.Snapshots[j].ProjectID, d.Snapshots[j].Name)
	})
	return d
}

func less2(p1, n1, p2, n2 string) bool {
	if p1 != p2 {
		return p1 < p2
	}
	return n1 < n2
}

func (c *computeInventory) run(project, kind string, fn func() error) SourceStatus {
	st := SourceStatus{Source: project + "/" + kind, Status: "ok"}
	err := fn()
	if err == nil {
		return st
	}
	cls := classifyComputeErr(err)
	m := cls.message
	switch {
	case cls.disabled:
		m = "compute.googleapis.com is not enabled on this project; no compute resources"
		st.Message = &m
	default:
		st.Status = "failed"
		st.Message = &m
		code := cls.code
		st.ErrorCode = &code
		h := cls.hint
		st.FixHint = &h
	}
	return st
}

type computeErr struct {
	disabled bool
	code     string
	message  string
	hint     string
}

func classifyComputeErr(err error) computeErr {
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		lower := strings.ToLower(ge.Message)
		for _, e := range ge.Errors {
			lower += " " + strings.ToLower(e.Reason)
		}
		for _, d := range ge.Details {
			lower += " " + strings.ToLower(fmt.Sprint(d))
		}
		switch {
		case ge.Code == http.StatusForbidden && (strings.Contains(lower, "has not been used") ||
			strings.Contains(lower, "is disabled") || strings.Contains(lower, "accessnotconfigured") ||
			strings.Contains(lower, "service_disabled")):
			return computeErr{disabled: true}
		case ge.Code == http.StatusForbidden || ge.Code == http.StatusUnauthorized:
			return computeErr{code: ErrCodePermissionDenied, message: "permission denied (HTTP " + fmt.Sprint(ge.Code) + ")",
				hint: "Grant roles/compute.viewer to the service account on this project."}
		}
		return computeErr{code: ErrCodeUpstream, message: fmt.Sprintf("compute API HTTP %d", ge.Code), hint: inventoryFixHint}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return computeErr{code: ErrCodeUpstream, message: "timed out", hint: "Retry; the Compute API did not answer in time."}
	}
	return computeErr{code: ErrCodeUpstream, message: "compute API request failed", hint: inventoryFixHint}
}

func lastSeg(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func segs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, lastSeg(s))
	}
	return out
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func copyLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func convertVM(project string, in *compute.Instance) VM {
	return VM{ProjectID: project, Name: in.Name, Zone: lastSeg(in.Zone), MachineType: lastSeg(in.MachineType),
		Status: in.Status, Labels: copyLabels(in.Labels)}
}

func convertDisk(project string, d *compute.Disk) DiskDetail {
	return DiskDetail{
		Disk: Disk{ProjectID: project, Name: d.Name, Zone: lastSeg(d.Zone), Status: d.Status,
			Users: segs(d.Users), SizeGB: d.SizeGb, Type: lastSeg(d.Type)},
		Labels: copyLabels(d.Labels), CreatedAt: parseTS(d.CreationTimestamp),
		LastAttachedAt: parseTS(d.LastAttachTimestamp), LastDetachedAt: parseTS(d.LastDetachTimestamp),
	}
}

func convertAddress(project string, a *compute.Address, global bool) AddressDetail {
	region := lastSeg(a.Region)
	if global || region == "" {
		region = "global"
	}
	return AddressDetail{
		Address: Address{ProjectID: project, Name: a.Name, Region: region, Status: a.Status,
			Users: segs(a.Users), Address: a.Address},
		Global: global || a.Region == "", Labels: copyLabels(a.Labels),
	}
}

func convertSnapshot(project string, s *compute.Snapshot) SnapshotDetail {
	return SnapshotDetail{
		Snapshot: Snapshot{ProjectID: project, Name: s.Name, CreatedAt: parseTS(s.CreationTimestamp),
			SourceDisk: lastSeg(s.SourceDisk), SizeGB: s.DiskSizeGb},
		Labels: copyLabels(s.Labels), StorageBytes: s.StorageBytes,
	}
}

func (c *computeInventory) listVMs(ctx context.Context, p string, d *InventoryDetail) error {
	return c.svc.Instances.AggregatedList(p).Pages(ctx, func(r *compute.InstanceAggregatedList) error {
		for _, sc := range r.Items {
			for _, in := range sc.Instances {
				d.VMs = append(d.VMs, convertVM(p, in))
			}
		}
		return nil
	})
}

func (c *computeInventory) listDisks(ctx context.Context, p string, d *InventoryDetail) error {
	return c.svc.Disks.AggregatedList(p).Pages(ctx, func(r *compute.DiskAggregatedList) error {
		for _, sc := range r.Items {
			for _, x := range sc.Disks {
				d.Disks = append(d.Disks, convertDisk(p, x))
			}
		}
		return nil
	})
}

func (c *computeInventory) listAddresses(ctx context.Context, p string, d *InventoryDetail) error {
	err := c.svc.Addresses.AggregatedList(p).Pages(ctx, func(r *compute.AddressAggregatedList) error {
		for _, sc := range r.Items {
			for _, x := range sc.Addresses {
				d.Addresses = append(d.Addresses, convertAddress(p, x, false))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.svc.GlobalAddresses.List(p).Pages(ctx, func(r *compute.AddressList) error {
		for _, x := range r.Items {
			d.Addresses = append(d.Addresses, convertAddress(p, x, true))
		}
		return nil
	})
}

func (c *computeInventory) listSnapshots(ctx context.Context, p string, d *InventoryDetail) error {
	return c.svc.Snapshots.List(p).Pages(ctx, func(r *compute.SnapshotList) error {
		for _, x := range r.Items {
			d.Snapshots = append(d.Snapshots, convertSnapshot(p, x))
		}
		return nil
	})
}
