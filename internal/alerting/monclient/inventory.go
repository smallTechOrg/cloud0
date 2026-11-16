package monclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	run "google.golang.org/api/run/v2"
	sqladmin "google.golang.org/api/sqladmin/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

const (
	opsAgentMetric  = "agent.googleapis.com/memory/percent_used"
	agentLookback   = 24 * time.Hour
	agentCacheTTL   = 5 * time.Minute
	invComputeScope = "https://www.googleapis.com/auth/cloud-platform"
)

// A project whose API is disabled has no such resources and is skipped.
// Inventory implements alerting.CoverageSource with READER credentials: VMs
// (Compute API), Cloud SQL instances (sqladmin), Cloud Run services (run API),
// policies (PolicyClient) and Ops Agent presence (Monitoring metric series).
type Inventory struct {
	projects []string
	policies alerting.PolicyClient
	compute  *compute.Service
	sql      *sqladmin.Service
	run      *run.Service
	metrics  *monitoring.MetricClient
	now      func() time.Time

	mu     sync.Mutex
	agents map[string]agentSet // project -> set
}

type agentSet struct {
	at  time.Time
	ids map[string]bool
}

var _ alerting.CoverageSource = (*Inventory)(nil)

// NewInventory builds the inventory. Only creds.Reader is used.
func NewInventory(ctx context.Context, projects []string, creds Creds, policies alerting.PolicyClient) (*Inventory, error) {
	ro := append(append([]option.ClientOption{}, creds.Reader...), option.WithScopes(invComputeScope))
	cs, err := compute.NewService(ctx, ro...)
	if err != nil {
		return nil, fmt.Errorf("%w: compute client: %v", alerting.ErrUpstream, err)
	}
	ss, err := sqladmin.NewService(ctx, ro...)
	if err != nil {
		return nil, fmt.Errorf("%w: sqladmin client: %v", alerting.ErrUpstream, err)
	}
	rs, err := run.NewService(ctx, ro...)
	if err != nil {
		return nil, fmt.Errorf("%w: run client: %v", alerting.ErrUpstream, err)
	}
	mc, err := monitoring.NewMetricClient(ctx, creds.Reader...)
	if err != nil {
		return nil, fmt.Errorf("%w: metric client: %v", alerting.ErrUpstream, err)
	}
	return &Inventory{
		projects: append([]string(nil), projects...), policies: policies,
		compute: cs, sql: ss, run: rs, metrics: mc, now: time.Now,
		agents: map[string]agentSet{},
	}, nil
}

// Close releases the gRPC metric client.
func (v *Inventory) Close() error { return v.metrics.Close() }

// invMapErr maps Google REST errors onto the alerting sentinels.
func invMapErr(err error, what, project, role string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		lower := strings.ToLower(ge.Message)
		switch {
		case ge.Code == http.StatusForbidden && (strings.Contains(lower, "has not been used") || strings.Contains(lower, "is disabled")):
			return fmt.Errorf("%w: %s API is not enabled on %s: %s", alerting.ErrAPIDisabled, what, project, ge.Message)
		case ge.Code == http.StatusForbidden || ge.Code == http.StatusUnauthorized:
			return fmt.Errorf("%w: grant %s to the reader service account on %s (%s): %s", alerting.ErrPermissionDenied, role, project, what, ge.Message)
		case ge.Code == http.StatusNotFound:
			return fmt.Errorf("%w: %s %s: %s", alerting.ErrNotFound, what, project, ge.Message)
		}
		return fmt.Errorf("%w: %s %s: HTTP %d: %s", alerting.ErrUpstream, what, project, ge.Code, ge.Message)
	}
	return fmt.Errorf("%w: %s %s: %v", alerting.ErrUpstream, what, project, err)
}

// VMs lists every Compute Engine instance of the configured projects.
func (v *Inventory) VMs(ctx context.Context) ([]alerting.VMInfo, error) {
	out := []alerting.VMInfo{}
	for _, p := range v.projects {
		err := v.compute.Instances.AggregatedList(p).Pages(ctx, func(r *compute.InstanceAggregatedList) error {
			for _, sc := range r.Items {
				for _, in := range sc.Instances {
					out = append(out, alerting.VMInfo{Project: p, Name: in.Name, ID: strconv.FormatUint(in.Id, 10)})
				}
			}
			return nil
		})
		if err != nil {
			if e := invMapErr(err, "compute.instances.aggregatedList", p, "roles/compute.viewer"); !errors.Is(e, alerting.ErrAPIDisabled) {
				return nil, e
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// CloudSQL lists the Cloud SQL instances of the configured projects.
func (v *Inventory) CloudSQL(ctx context.Context) ([]alerting.DBInfo, error) {
	out := []alerting.DBInfo{}
	for _, p := range v.projects {
		err := v.sql.Instances.List(p).Pages(ctx, func(r *sqladmin.InstancesListResponse) error {
			for _, in := range r.Items {
				tier := ""
				if in.Settings != nil {
					tier = in.Settings.Tier
				}
				out = append(out, alerting.DBInfo{Project: p, Name: in.Name, Tier: tier})
			}
			return nil
		})
		if err != nil {
			if e := invMapErr(err, "sqladmin.instances.list", p, "roles/cloudsql.viewer"); !errors.Is(e, alerting.ErrAPIDisabled) {
				return nil, e
			}
		}
	}
	return out, nil
}

// CloudRun lists the Cloud Run services of the configured projects (all regions).
func (v *Inventory) CloudRun(ctx context.Context) ([]alerting.ServiceInfo, error) {
	out := []alerting.ServiceInfo{}
	for _, p := range v.projects {
		err := v.run.Projects.Locations.Services.List("projects/"+p+"/locations/-").Pages(ctx, func(r *run.GoogleCloudRunV2ListServicesResponse) error {
			for _, s := range r.Services {
				name := s.Name
				if i := strings.LastIndex(name, "/"); i >= 0 {
					name = name[i+1:]
				}
				out = append(out, alerting.ServiceInfo{Project: p, Name: name})
			}
			return nil
		})
		if err != nil {
			if e := invMapErr(err, "run.services.list", p, "roles/run.viewer"); !errors.Is(e, alerting.ErrAPIDisabled) {
				return nil, e
			}
		}
	}
	return out, nil
}

// Policies lists the alert policies of every configured project.
func (v *Inventory) Policies(ctx context.Context) ([]alerting.PolicyView, error) {
	out := []alerting.PolicyView{}
	for _, p := range v.projects {
		ps, err := v.policies.List(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, ps...)
	}
	return out, nil
}

// HasOpsAgent reports whether the VM wrote agent.googleapis.com/memory/percent_used
// in the last 24 h. Series per project are cached for a few minutes.
func (v *Inventory) HasOpsAgent(ctx context.Context, vm alerting.VMInfo) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	set, ok := v.agents[vm.Project]
	if !ok || v.now().Sub(set.at) > agentCacheTTL {
		ids, err := v.listAgentInstances(ctx, vm.Project)
		if err != nil {
			return false, err
		}
		set = agentSet{at: v.now(), ids: ids}
		v.agents[vm.Project] = set
	}
	return set.ids[vm.ID], nil
}

func (v *Inventory) listAgentInstances(ctx context.Context, project string) (map[string]bool, error) {
	end := v.now().UTC()
	it := v.metrics.ListTimeSeries(ctx, &monitoringpb.ListTimeSeriesRequest{
		Name:   "projects/" + project,
		Filter: fmt.Sprintf(`metric.type="%s" AND resource.type="gce_instance"`, opsAgentMetric),
		Interval: &monitoringpb.TimeInterval{
			StartTime: timestamppb.New(end.Add(-agentLookback)),
			EndTime:   timestamppb.New(end),
		},
		View: monitoringpb.ListTimeSeriesRequest_HEADERS,
	})
	ids := map[string]bool{}
	for {
		ts, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, mapErr(err, false, "", project)
		}
		if id := ts.GetResource().GetLabels()["instance_id"]; id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}
