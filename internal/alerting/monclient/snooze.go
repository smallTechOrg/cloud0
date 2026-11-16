package monclient

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

const (
	// SnoozeMinSeconds / SnoozeMaxSeconds bound SnoozeDraft.DurationSeconds
	// (frozen contract: 300 s .. 30 days).
	SnoozeMinSeconds = 300
	SnoozeMaxSeconds = 30 * 24 * 3600
)

var (
	snoozeNameRE = regexp.MustCompile(`^projects/([^/]+)/snoozes/([^/]+)$`)
	policyNameRE = regexp.MustCompile(`^projects/([^/]+)/alertPolicies/[^/]+$`)
)

// SnoozeClient implements alerting.SnoozeClient over the Monitoring SnoozeClient.
type SnoozeClient struct {
	reader *monitoring.SnoozeClient
	writer *monitoring.SnoozeClient
	now    func() time.Time
}

var _ alerting.SnoozeClient = (*SnoozeClient)(nil)

// NewSnoozeClient builds the snooze client from the shared credentials.
func NewSnoozeClient(ctx context.Context, c Creds) (*SnoozeClient, error) {
	r, err := monitoring.NewSnoozeClient(ctx, c.Reader...)
	if err != nil {
		return nil, fmt.Errorf("%w: snooze reader client: %v", alerting.ErrUpstream, err)
	}
	s := &SnoozeClient{reader: r, now: time.Now}
	if len(c.Writer) > 0 {
		w, err := monitoring.NewSnoozeClient(ctx, c.Writer...)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("%w: snooze writer client: %v", alerting.ErrUpstream, err)
		}
		s.writer = w
	}
	return s, nil
}

// Close releases both connections.
func (s *SnoozeClient) Close() error {
	var errs []error
	if s.reader != nil {
		errs = append(errs, s.reader.Close())
	}
	if s.writer != nil {
		errs = append(errs, s.writer.Close())
	}
	return errors.Join(errs...)
}

// List returns the project's snoozes, newest first.
func (s *SnoozeClient) List(ctx context.Context, project string) ([]alerting.Snooze, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("%w: project is required", alerting.ErrInvalidParams)
	}
	now := s.now()
	out := []alerting.Snooze{}
	it := s.reader.ListSnoozes(ctx, &monitoringpb.ListSnoozesRequest{Parent: "projects/" + project})
	for {
		sn, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, mapErr(err, false, "", project)
		}
		out = append(out, snoozeView(sn, now))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartTime.After(out[j].StartTime) })
	return out, nil
}

// Create validates the draft and creates the snooze.
func (s *SnoozeClient) Create(ctx context.Context, d alerting.SnoozeDraft) (alerting.Snooze, error) {
	now := s.now()
	start, end, err := ValidateSnoozeDraft(d, now)
	if err != nil {
		return alerting.Snooze{}, err
	}
	if s.writer == nil {
		return alerting.Snooze{}, alerting.ErrNotConfigured
	}
	sn, err := s.writer.CreateSnooze(ctx, &monitoringpb.CreateSnoozeRequest{
		Parent: "projects/" + d.Project,
		Snooze: &monitoringpb.Snooze{
			DisplayName: strings.TrimSpace(d.DisplayName),
			Criteria:    &monitoringpb.Snooze_Criteria{Policies: d.PolicyNames},
			Interval:    &monitoringpb.TimeInterval{StartTime: timestamppb.New(start), EndTime: timestamppb.New(end)},
		},
	})
	if err != nil {
		return alerting.Snooze{}, mapErr(err, true, usRoleSnoozeEditor, d.Project)
	}
	return snoozeView(sn, now), nil
}

// End sets the snooze end time to now (there is no delete); a snooze younger
// than a minute is cancelled (end = start), which Monitoring treats the same. An already expired
// snooze, or one that has not started yet, is rejected with ErrInvalidParams.
func (s *SnoozeClient) End(ctx context.Context, name string, now time.Time) (alerting.Snooze, error) {
	m := snoozeNameRE.FindStringSubmatch(name)
	if m == nil {
		return alerting.Snooze{}, fmt.Errorf("%w: snooze name must look like projects/<project>/snoozes/<id>", alerting.ErrInvalidParams)
	}
	proj := m[1]
	if now.IsZero() {
		now = s.now()
	}
	if s.writer == nil {
		return alerting.Snooze{}, alerting.ErrNotConfigured
	}
	cur, err := s.writer.GetSnooze(ctx, &monitoringpb.GetSnoozeRequest{Name: name})
	if err != nil {
		return alerting.Snooze{}, mapErr(err, true, usRoleSnoozeEditor, proj)
	}
	v := snoozeView(cur, now)
	switch v.State {
	case "expired":
		return alerting.Snooze{}, fmt.Errorf("%w: snooze %s has already ended", alerting.ErrInvalidParams, name)
	case "scheduled":
		return alerting.Snooze{}, fmt.Errorf("%w: snooze %s has not started yet; it cannot be ended before its start", alerting.ErrInvalidParams, name)
	}
	// Monitoring rules: a snooze must last >= 1 minute unless it is cancelled;
	// cancelling an active snooze means end == start, cancelling an upcoming one
	// means start == end == now (and the server's notion of "upcoming" lags the
	// clock by seconds to minutes). Try the candidates in order.
	nowT := now.UTC().Truncate(time.Second)
	type attempt struct {
		start, end time.Time
		paths      []string
	}
	var attempts []attempt
	if nowT.Sub(v.StartTime) >= time.Minute {
		attempts = append(attempts, attempt{v.StartTime, nowT, []string{"interval.end_time"}})
	}
	attempts = append(attempts,
		attempt{v.StartTime, v.StartTime, []string{"interval.end_time"}},
		attempt{nowT, nowT, []string{"interval.start_time", "interval.end_time"}},
	)
	var lastErr error
	for _, at := range attempts {
		upd := proto.Clone(cur).(*monitoringpb.Snooze)
		upd.Interval.StartTime = timestamppb.New(at.start)
		upd.Interval.EndTime = timestamppb.New(at.end)
		got, err := s.writer.UpdateSnooze(ctx, &monitoringpb.UpdateSnoozeRequest{
			Snooze:     upd,
			UpdateMask: &fieldmaskpb.FieldMask{Paths: at.paths},
		})
		if err == nil {
			return snoozeView(got, now), nil
		}
		lastErr = mapErr(err, true, usRoleSnoozeEditor, proj)
		if !errors.Is(lastErr, alerting.ErrInvalidParams) {
			break
		}
	}
	return alerting.Snooze{}, lastErr
}

// ValidateSnoozeDraft checks the draft and returns the resolved start/end.
// Errors wrap ErrInvalidParams.
func ValidateSnoozeDraft(d alerting.SnoozeDraft, now time.Time) (start, end time.Time, err error) {
	bad := func(format string, a ...any) (time.Time, time.Time, error) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: %s", alerting.ErrInvalidParams, fmt.Sprintf(format, a...))
	}
	if strings.TrimSpace(d.Project) == "" {
		return bad("project is required")
	}
	if strings.TrimSpace(d.DisplayName) == "" || len(d.DisplayName) > 100 {
		return bad("display_name is required (max 100 characters)")
	}
	if len(d.PolicyNames) == 0 {
		return bad("policy_names must not be empty")
	}
	for _, p := range d.PolicyNames {
		m := policyNameRE.FindStringSubmatch(p)
		if m == nil {
			return bad("policy name %q must look like projects/<project>/alertPolicies/<id>", p)
		}
		if m[1] != d.Project {
			return bad("policy %q is not in project %q", p, d.Project)
		}
	}
	if d.DurationSeconds < SnoozeMinSeconds || d.DurationSeconds > SnoozeMaxSeconds {
		return bad("duration_seconds must be %d..%d (5 minutes to 30 days)", SnoozeMinSeconds, SnoozeMaxSeconds)
	}
	start = now
	if d.StartTime != nil {
		start = *d.StartTime
	}
	start = start.UTC().Truncate(time.Second)
	if start.Before(now.Add(-24 * time.Hour)) {
		return bad("start_time is more than a day in the past")
	}
	end = start.Add(time.Duration(d.DurationSeconds) * time.Second)
	if !end.After(start) {
		return bad("end must be after start")
	}
	return start, end, nil
}

func snoozeView(sn *monitoringpb.Snooze, now time.Time) alerting.Snooze {
	v := alerting.Snooze{
		Name:        sn.GetName(),
		DisplayName: sn.GetDisplayName(),
		PolicyNames: append([]string{}, sn.GetCriteria().GetPolicies()...),
	}
	if m := snoozeNameRE.FindStringSubmatch(sn.GetName()); m != nil {
		v.Project = m[1]
	}
	if iv := sn.GetInterval(); iv != nil {
		v.StartTime = iv.GetStartTime().AsTime()
		v.EndTime = iv.GetEndTime().AsTime()
	}
	v.State = snoozeState(v.StartTime, v.EndTime, now)
	return v
}

func snoozeState(start, end, now time.Time) string {
	switch {
	case !now.Before(end):
		return "expired"
	case now.Before(start):
		return "scheduled"
	}
	return "active"
}
