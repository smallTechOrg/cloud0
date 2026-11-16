package httpapi

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// Daily cost (SMA-29): GET /api/v1/cost/daily serves net cost per UTC day,
// split into series by service, project, product or environment, from the
// per-day rows stored with each week's latest snapshot.

const (
	dailyDefaultDays = 28
	dailyMaxDays     = 366
	dailyDefaultTop  = 8
	dailyMaxTop      = 20
	dailyOtherKey    = "(other)"
)

// Daily breakdown dimensions (query parameter `by`).
var dailyDims = map[string]bool{"service": true, "project": true, "product": true, "environment": true}

// DailyDay is one x-axis entry. Total is nil when the day has no data: its
// week was never fetched (Snapshot nil) or was fetched before daily rows were
// captured (HasDaily false) — never a zero.
type DailyDay struct {
	Date       string         `json:"date"`
	Week       string         `json:"week"`
	WeekStatus isoweek.Status `json:"week_status"`
	SnapshotID *int64         `json:"snapshot_id"`
	HasDaily   bool           `json:"has_daily"`
	Total      *float64       `json:"total"`
}

// DailySeries is one stacked series; Values align with Days (nil = no data).
type DailySeries struct {
	Key    string     `json:"key"`
	Label  string     `json:"label"`
	Kind   string     `json:"kind"` // group | other
	Total  float64    `json:"total"`
	Values []*float64 `json:"values"`
}

// DailyResponse is the GET /api/v1/cost/daily body.
type DailyResponse struct {
	From        string        `json:"from"`
	To          string        `json:"to"`
	Scope       string        `json:"scope"`
	By          string        `json:"by"`
	Top         int           `json:"top"`
	GeneratedAt time.Time     `json:"generated_at"`
	Days        []DailyDay    `json:"days"`
	Series      []DailySeries `json:"series"`
	Total       float64       `json:"total"`
	Notes       []string      `json:"notes"`
}

func errInvalidDate(param, v string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: "invalid_date",
		Message: "The " + param + " date " + quote(v) + " is not a valid date."}).WithHint("Use YYYY-MM-DD (UTC), for example 2026-10-12.")
}

func errDateRange(msg string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: "date_out_of_range", Message: msg}).
		WithHint("Pick dates from the first week's Monday (GET /api/v1/config → first_week) up to today, at most 366 days apart.")
}

func (d Deps) registerDaily(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/cost/daily", d.handleDaily)
}

func parseDay(param, v string) (time.Time, *APIError) {
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}, errInvalidDate(param, v)
	}
	return t.UTC(), nil
}

func (d Deps) handleDaily(w http.ResponseWriter, r *http.Request) *APIError {
	ctx := r.Context()
	q := r.URL.Query()
	now := d.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	firstDay := d.Config.FirstWeek.Start()

	scope, aerr := d.scopeParam(q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	by := q.Get("by")
	if by == "" {
		by = "service"
	}
	if !dailyDims[by] {
		return (&APIError{Status: http.StatusBadRequest, Code: "invalid_dimension", Message: "The by value " + quote(by) + " is not valid."}).
			WithHint("Use by=service, by=project, by=product or by=environment.")
	}
	top := dailyDefaultTop
	if v := q.Get("top"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > dailyMaxTop {
			return (&APIError{Status: http.StatusBadRequest, Code: "invalid_top", Message: "The top value " + quote(v) + " is not valid."}).
				WithHint("Use an integer from 1 to 20: the number of series shown before the rest is grouped as (other).")
		}
		top = n
	}
	to := today
	if v := q.Get("to"); v != "" {
		if to, aerr = parseDay("to", v); aerr != nil {
			return aerr
		}
	}
	span := dailyDefaultDays
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > dailyMaxDays {
			return (&APIError{Status: http.StatusBadRequest, Code: "invalid_days", Message: "The days value " + quote(v) + " is not valid."}).
				WithHint("Use an integer from 1 to 366: the number of days ending at `to` (ignored when `from` is given).")
		}
		span = n
	}
	from := to.AddDate(0, 0, -(span - 1))
	if v := q.Get("from"); v != "" {
		if from, aerr = parseDay("from", v); aerr != nil {
			return aerr
		}
	} else if from.Before(firstDay) {
		from = firstDay
	}
	switch {
	case to.After(today):
		return errDateRange("The to date " + store.FormatDate(to) + " is after today (" + store.FormatDate(today) + ", UTC).")
	case from.Before(firstDay):
		return errDateRange("The from date " + store.FormatDate(from) + " is before the first week's Monday " + store.FormatDate(firstDay) + ".")
	case from.After(to):
		return errDateRange("The from date " + store.FormatDate(from) + " is after the to date " + store.FormatDate(to) + ".")
	case to.Sub(from) >= dailyMaxDays*24*time.Hour:
		return errDateRange("The range " + store.FormatDate(from) + " … " + store.FormatDate(to) + " is longer than 366 days.")
	}

	var eng attribution.Engine
	if by == "product" || by == "environment" {
		if eng, aerr = d.bdEngine(ctx); aerr != nil {
			return aerr
		}
	}

	latest, err := store.LatestPerWeek(ctx, d.Store.DB, isoweek.Of(from), isoweek.Of(to))
	if err != nil {
		return ErrInternal(err)
	}
	ids := make([]int64, 0, len(latest))
	for _, s := range latest {
		ids = append(ids, s.ID)
	}
	withDaily, err := store.SnapshotsWithDaily(ctx, d.Store.DB, ids)
	if err != nil {
		return ErrInternal(err)
	}
	rows, err := store.DailyRowsForSnapshots(ctx, d.Store.DB, ids, store.FormatDate(from), store.FormatDate(to), scope)
	if err != nil {
		return ErrInternal(err)
	}

	out := DailyResponse{From: store.FormatDate(from), To: store.FormatDate(to), Scope: scope, By: by, Top: top,
		GeneratedAt: now.Truncate(time.Second), Days: []DailyDay{}, Series: []DailySeries{}, Notes: []string{}}
	dayIndex := map[string]int{}
	var notFetched, noDaily []string
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		wk := isoweek.Of(day)
		dd := DailyDay{Date: store.FormatDate(day), Week: wk.String(), WeekStatus: isoweek.StatusAt(wk, now, d.settle())}
		if snap, ok := latest[wk]; ok {
			id := snap.ID
			dd.SnapshotID = &id
			dd.HasDaily = withDaily[id] || snap.RowCount == 0 // an empty week is a real zero
			if dd.HasDaily {
				zero := 0.0
				dd.Total = &zero
			} else if len(noDaily) == 0 || noDaily[len(noDaily)-1] != wk.String() {
				noDaily = append(noDaily, wk.String())
			}
		} else if len(notFetched) == 0 || notFetched[len(notFetched)-1] != wk.String() {
			notFetched = append(notFetched, wk.String())
		}
		dayIndex[dd.Date] = len(out.Days)
		out.Days = append(out.Days, dd)
	}

	// Series key and label per row.
	labels := map[string]string{}
	if by == "service" {
		for _, c := range costtable.ColumnsFor(nil) {
			if c.Kind == costtable.KindService {
				labels[c.Key] = c.Label
			}
		}
	}
	keyOf := func(dr store.DailyRow) (string, string) {
		switch by {
		case "project":
			return dr.ProjectID, dr.ProjectID
		case "service":
			k := costtable.ColumnKeyFor(dr.Service)
			if l, ok := labels[k]; ok {
				return k, l
			}
			return k, strings.TrimSpace(dr.Service)
		}
		row := attribution.Row{ProjectID: dr.ProjectID, Service: dr.Service, SKU: dr.SKU, Labels: bdLabels(dr.LabelsJSON)}
		if dr.ResourceName != nil {
			row.ResourceName = *dr.ResourceName
		}
		if dr.ResourceGlobalName != nil {
			row.ResourceGlobalName = *dr.ResourceGlobalName
		}
		ev := eng.Evaluate(row)
		if by == "environment" {
			if ev.Environment == "" {
				return attribution.KeyNoEnvironment, attribution.KeyNoEnvironment
			}
			return ev.Environment, ev.Environment
		}
		switch {
		case ev.Shared:
			return attribution.KeyShared, attribution.KeyShared
		case ev.Product == "":
			return attribution.KeyUnattributed, attribution.KeyUnattributed
		}
		return ev.Product, ev.Product
	}

	type acc struct {
		label  string
		total  float64
		values map[int]float64
	}
	series := map[string]*acc{}
	for _, dr := range rows {
		i, ok := dayIndex[dr.UsageDate]
		if !ok || !out.Days[i].HasDaily {
			continue
		}
		k, l := keyOf(dr)
		a := series[k]
		if a == nil {
			a = &acc{label: l, values: map[int]float64{}}
			series[k] = a
		}
		a.values[i] += dr.Net
		a.total += dr.Net
		*out.Days[i].Total += dr.Net
		out.Total += dr.Net
	}

	keys := make([]string, 0, len(series))
	for k, a := range series {
		if a.total != 0 || len(a.values) > 0 {
			keys = append(keys, k)
		}
	}
	residue := func(k string) bool {
		return k == attribution.KeyShared || k == attribution.KeyUnattributed || k == attribution.KeyNoEnvironment
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := residue(keys[i]), residue(keys[j]); ri != rj {
			return rj // named groups before the residue buckets
		} else if ri {
			return keys[i] == attribution.KeyShared // (shared) before (unattributed), as in the weekly table
		}
		a, b := series[keys[i]].total, series[keys[j]].total
		if math.Abs(a-b) > 1e-9 {
			return a > b
		}
		return keys[i] < keys[j]
	})
	mk := func(k, label, kind string, total float64, vals map[int]float64) DailySeries {
		s := DailySeries{Key: k, Label: label, Kind: kind, Total: total, Values: make([]*float64, len(out.Days))}
		for i, dd := range out.Days {
			if dd.HasDaily {
				v := vals[i]
				s.Values[i] = &v
			}
		}
		return s
	}
	other := &acc{values: map[int]float64{}}
	shown := 0
	for _, k := range keys {
		a := series[k]
		if shown < top || residue(k) {
			out.Series = append(out.Series, mk(k, a.label, "group", a.total, a.values))
			if !residue(k) {
				shown++
			}
			continue
		}
		other.total += a.total
		for i, v := range a.values {
			other.values[i] += v
		}
	}
	if len(other.values) > 0 {
		// (other) sits after the named groups, before the residue buckets.
		at := len(out.Series)
		for at > 0 && residue(out.Series[at-1].Key) {
			at--
		}
		o := mk(dailyOtherKey, "Other", "other", other.total, other.values)
		out.Series = append(out.Series[:at], append([]DailySeries{o}, out.Series[at:]...)...)
	}

	if len(notFetched) > 0 {
		out.Notes = append(out.Notes, "Not fetched yet: "+strings.Join(notFetched, ", ")+" — refresh those weeks to see their days.")
	}
	if len(noDaily) > 0 {
		out.Notes = append(out.Notes, "Fetched before daily data was captured: "+strings.Join(noDaily, ", ")+" — refresh those weeks once to fill their days.")
	}
	if by == "product" {
		out.Notes = append(out.Notes, "(shared) is shown as its own series; daily views do not allocate it.")
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
