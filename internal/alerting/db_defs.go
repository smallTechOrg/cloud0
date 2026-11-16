package alerting

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Database + uptime templates: sql_down, sql_cpu_high, sql_memory_high,
// sql_disk_high and uptime_down (which yields two managed policies, told apart
// by TemplateParams.Instance). Everything here is pure: no I/O.

const (
	dbInstanceWarning  = "warning"
	dbInstanceCritical = "critical"

	dbMetricSQLUp      = "cloudsql.googleapis.com/database/up"
	dbMetricSQLCPU     = "cloudsql.googleapis.com/database/cpu/utilization"
	dbMetricSQLMemUse  = "cloudsql.googleapis.com/database/memory/total_usage"
	dbMetricSQLMemQuot = "cloudsql.googleapis.com/database/memory/quota"
	dbMetricSQLDisk    = "cloudsql.googleapis.com/database/disk/utilization"
	dbMetricUptime     = "monitoring.googleapis.com/uptime_check/check_passed"
)

func dbF(v float64) *float64 { return &v }
func dbI(v int) *int         { return &v }
func dbB(v bool) *bool       { return &v }

func dbDurationSpec() ParamSpec {
	return ParamSpec{Key: "duration_seconds", Label: "For", Type: "int", Unit: "seconds",
		Min: dbF(60), Max: dbF(86400), Options: []string{},
		Help: "How long the condition must hold before the alert opens (whole minutes)."}
}

func dbSeveritySpec() ParamSpec {
	return ParamSpec{Key: "severity", Label: "Severity", Type: "enum", Options: append([]string{}, Severities...),
		Help: "Monitoring policy severity; also selects the notification channels via the severity routing."}
}

func dbEnabledSpec() ParamSpec {
	return ParamSpec{Key: "enabled", Label: "Enabled", Type: "bool", Options: []string{}}
}

func dbPercentSpec(label, help string) ParamSpec {
	return ParamSpec{Key: "threshold", Label: label, Type: "number", Unit: "%",
		Min: dbF(1), Max: dbF(100), Options: []string{}, Help: help}
}

func dbDefs() []templateDef {
	return []templateDef{dbSQLDownDef(), dbSQLUtilDef(sqlUtilCPU), dbSQLMemoryDef(), dbSQLUtilDef(sqlUtilDisk), dbUptimeDef()}
}

// ---- shared helpers --------------------------------------------------------

type dbMap = map[string]any

// dbRatio converts a percent to a ratio without float noise (85 -> 0.85).
func dbRatio(percent float64) float64 { return math.Round(percent/100*1e6) / 1e6 }

func dbHumanDuration(sec int) string {
	switch {
	case sec%3600 == 0:
		return fmt.Sprintf("%d h", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%d min", sec/60)
	}
	return fmt.Sprintf("%d s", sec)
}

func dbValidate(p TemplateParams, threshold bool) error {
	if p.DurationSeconds == nil {
		return fmt.Errorf("%w: duration_seconds is required", ErrInvalidParams)
	}
	if d := *p.DurationSeconds; d < 60 || d > 86400 || d%60 != 0 {
		return fmt.Errorf("%w: duration_seconds %d must be a multiple of 60 between 60 and 86400", ErrInvalidParams, d)
	}
	if threshold && p.Threshold == nil {
		return fmt.Errorf("%w: threshold is required", ErrInvalidParams)
	}
	if !threshold && p.Threshold != nil {
		return fmt.Errorf("%w: threshold is not editable for this template", ErrInvalidParams)
	}
	return nil
}

// dbPolicy assembles the AlertPolicy REST body shared by all templates here.
func dbPolicy(displayName, doc string, p TemplateParams, ctx BuildContext, tplID, instance string, cond dbMap) (json.RawMessage, error) {
	labels := dbMap{LabelManagedBy: LabelManagedByVal, LabelTemplate: tplID}
	if instance != "" {
		labels[LabelInstance] = instance
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	pol := dbMap{
		"displayName":   displayName,
		"documentation": dbMap{"content": doc, "mimeType": "text/markdown"},
		"userLabels":    labels,
		"conditions":    []any{cond},
		"combiner":      "OR",
		"enabled":       enabled,
		"severity":      p.Severity,
	}
	if len(ctx.Channels) > 0 {
		pol["notificationChannels"] = append([]string{}, ctx.Channels...)
	}
	return json.Marshal(pol)
}

func dbAgg(aligner, reducer string, groups ...string) dbMap {
	a := dbMap{"alignmentPeriod": "60s", "perSeriesAligner": aligner}
	if reducer != "" {
		a["crossSeriesReducer"] = reducer
		a["groupByFields"] = groups
	}
	return a
}

// ---- sql_down --------------------------------------------------------------

func dbSQLDownDef() templateDef {
	t := Template{
		ID: TplSQLDown, Kind: KindThreshold, Title: "Cloud SQL down",
		Description: "A Cloud SQL database reports up < 1 (or stops reporting) for the configured time, on any instance including future ones.",
		Severity:    SevCritical, DefaultEnabled: true,
		ParamSpecs: []ParamSpec{dbDurationSpec(), dbSeveritySpec(), dbEnabledSpec()},
		Defaults:   TemplateParams{DurationSeconds: dbI(180), Severity: SevCritical, Enabled: dbB(true)},
		Installed:  []string{}, CoveredResources: []CoveredResource{},
	}
	return templateDef{Template: t, Build: func(p TemplateParams, ctx BuildContext) (Built, error) {
		if err := dbValidate(p, false); err != nil {
			return Built{}, err
		}
		dur := *p.DurationSeconds
		cond := dbMap{
			"displayName": fmt.Sprintf("Cloud SQL database up < 1 for %s", dbHumanDuration(dur)),
			"conditionThreshold": dbMap{
				"filter":                fmt.Sprintf(`resource.type = "cloudsql_database" AND metric.type = %q`, dbMetricSQLUp),
				"aggregations":          []any{dbAgg("ALIGN_MIN", "REDUCE_MIN", "resource.label.database_id")},
				"comparison":            "COMPARISON_LT",
				"thresholdValue":        1,
				"duration":              fmt.Sprintf("%ds", dur),
				"trigger":               dbMap{"count": 1},
				"evaluationMissingData": "EVALUATION_MISSING_DATA_ACTIVE",
			},
		}
		doc := fmt.Sprintf("**Cloud SQL database is down.**\n\n"+
			"`cloudsql.googleapis.com/database/up` has been below 1 (or has stopped reporting, which is treated as a breach) for %s on a database in this project.\n\n"+
			"What to check:\n- Cloud SQL instance state and recent operations in the console (restart, maintenance, failover).\n- Instance logs for crashes or out-of-memory kills.\n- Disk full or memory exhaustion (see the Cloud SQL disk/memory alerts).\n\n"+
			"Managed by cloud0 (template `%s`); edit it from the Alerts tab, not in the console.", dbHumanDuration(dur), TplSQLDown)
		raw, err := dbPolicy("cloud0: "+t.Title, doc, p, ctx, TplSQLDown, "", cond)
		return Built{Policy: raw}, err
	}}
}

// ---- sql_cpu_high / sql_disk_high -----------------------------------------

type sqlUtil struct {
	id, title, label, metric, what, fix string
	severity                            string
}

var (
	sqlUtilCPU = sqlUtil{TplSQLCPUHigh, "Cloud SQL CPU high", "CPU %", dbMetricSQLCPU, "CPU utilization",
		"Look for slow or runaway queries (Query Insights), connection storms, or an undersized tier.", SevWarning}
	sqlUtilDisk = sqlUtil{TplSQLDiskHigh, "Cloud SQL disk high", "Disk %", dbMetricSQLDisk, "disk utilization",
		"Free space (purge/vacuum, drop old data) or enable automatic storage increase before the disk fills and the database stops accepting writes.", SevError}
)

func dbSQLUtilDef(u sqlUtil) templateDef {
	t := Template{
		ID: u.id, Kind: KindThreshold, Title: u.title,
		Description: fmt.Sprintf("Cloud SQL %s above the threshold on any database, including future ones.", u.what),
		Severity:    u.severity, DefaultEnabled: true,
		ParamSpecs: []ParamSpec{dbPercentSpec(u.label, ""), dbDurationSpec(), dbSeveritySpec(), dbEnabledSpec()},
		Defaults:   TemplateParams{Threshold: dbF(map[string]float64{TplSQLCPUHigh: 80, TplSQLDiskHigh: 85}[u.id]), DurationSeconds: dbI(300), Severity: u.severity, Enabled: dbB(true)},
		Installed:  []string{}, CoveredResources: []CoveredResource{},
	}
	return templateDef{Template: t, Build: func(p TemplateParams, ctx BuildContext) (Built, error) {
		if err := dbValidate(p, true); err != nil {
			return Built{}, err
		}
		dur, thr := *p.DurationSeconds, *p.Threshold
		cond := dbMap{
			"displayName": fmt.Sprintf("Cloud SQL %s > %g %% for %s", u.what, thr, dbHumanDuration(dur)),
			"conditionThreshold": dbMap{
				"filter":         fmt.Sprintf(`resource.type = "cloudsql_database" AND metric.type = %q`, u.metric),
				"aggregations":   []any{dbAgg("ALIGN_MEAN", "REDUCE_MAX", "resource.label.database_id")},
				"comparison":     "COMPARISON_GT",
				"thresholdValue": dbRatio(thr),
				"duration":       fmt.Sprintf("%ds", dur),
				"trigger":        dbMap{"count": 1},
			},
		}
		doc := fmt.Sprintf("**Cloud SQL %s is above %g %%** for %s on a database in this project.\n\n%s\n\n"+
			"Managed by cloud0 (template `%s`); edit it from the Alerts tab, not in the console.", u.what, thr, dbHumanDuration(dur), u.fix, u.id)
		raw, err := dbPolicy("cloud0: "+u.title, doc, p, ctx, u.id, "", cond)
		return Built{Policy: raw}, err
	}}
}

// ---- sql_memory_high -------------------------------------------------------

func dbSQLMemoryDef() templateDef {
	t := Template{
		ID: TplSQLMemoryHigh, Kind: KindRatio, Title: "Cloud SQL memory high",
		Description: "Cloud SQL memory usage (total_usage / quota) above the threshold on any database, including future ones. Shared-core tiers are skipped.",
		Severity:    SevError, DefaultEnabled: true,
		ParamSpecs: []ParamSpec{dbPercentSpec("Memory %", "Ratio of memory/total_usage to memory/quota."), dbDurationSpec(), dbSeveritySpec(), dbEnabledSpec()},
		Defaults:   TemplateParams{Threshold: dbF(85), DurationSeconds: dbI(300), Severity: SevError, Enabled: dbB(true)},
		Installed:  []string{}, CoveredResources: []CoveredResource{},
	}
	return templateDef{Template: t, Build: func(p TemplateParams, ctx BuildContext) (Built, error) {
		if err := dbValidate(p, true); err != nil {
			return Built{}, err
		}
		dur, thr := *p.DurationSeconds, *p.Threshold
		agg := func() []any { return []any{dbAgg("ALIGN_MEAN", "REDUCE_SUM", "resource.label.database_id")} }
		cond := dbMap{
			"displayName": fmt.Sprintf("Cloud SQL memory usage / quota > %g %% for %s", thr, dbHumanDuration(dur)),
			"conditionThreshold": dbMap{
				"filter":                  fmt.Sprintf(`resource.type = "cloudsql_database" AND metric.type = %q`, dbMetricSQLMemUse),
				"aggregations":            agg(),
				"denominatorFilter":       fmt.Sprintf(`resource.type = "cloudsql_database" AND metric.type = %q`, dbMetricSQLMemQuot),
				"denominatorAggregations": agg(),
				"comparison":              "COMPARISON_GT",
				"thresholdValue":          dbRatio(thr),
				"duration":                fmt.Sprintf("%ds", dur),
				"trigger":                 dbMap{"count": 1},
			},
		}
		doc := fmt.Sprintf("**Cloud SQL memory usage is above %g %% of the instance memory** for %s.\n\n"+
			"The condition is `database/memory/total_usage` divided by `database/memory/quota`. "+
			"It deliberately does not use `database/memory/utilization`, which reads 100 %% permanently on shared-core tiers (db-f1-micro, db-g1-small) and would alert forever.\n\n"+
			"`total_usage` includes the buffer/cache, so a high value can be normal for a busy database; check swap/OOM events and query latency before resizing.\n\n"+
			"Managed by cloud0 (template `%s`); edit it from the Alerts tab, not in the console.", thr, dbHumanDuration(dur), TplSQLMemoryHigh)
		raw, err := dbPolicy("cloud0: "+t.Title, doc, p, ctx, TplSQLMemoryHigh, "", cond)
		return Built{Policy: raw}, err
	}}
}

// ---- uptime_down -----------------------------------------------------------

func dbUptimeDef() templateDef {
	t := Template{
		ID: TplUptimeDown, Kind: KindUptime, Title: "Service down",
		Description: "An uptime check fails for the configured time. Two managed policies: instance \"warning\" (10 min, WARNING) and \"critical\" (2 h, CRITICAL).",
		Severity:    SevCritical, DefaultEnabled: true,
		ParamSpecs: []ParamSpec{
			dbDurationSpec(), dbSeveritySpec(), dbEnabledSpec(),
			{Key: "instance", Label: "Escalation level", Type: "enum", Options: []string{dbInstanceWarning, dbInstanceCritical},
				Help: "warning = early (10 min) notice, critical = prolonged (2 h) outage; each is its own policy."},
		},
		// Defaults describe the "warning" instance; Build switches to the critical
		// defaults when Instance is "critical" and these were left untouched.
		Defaults:  TemplateParams{DurationSeconds: dbI(600), Severity: SevWarning, Enabled: dbB(true), Instance: dbInstanceWarning},
		Installed: []string{}, CoveredResources: []CoveredResource{},
	}
	return templateDef{Template: t, Build: func(p TemplateParams, ctx BuildContext) (Built, error) {
		inst := p.Instance
		if inst == "" {
			inst = dbInstanceWarning
		}
		if inst != dbInstanceWarning && inst != dbInstanceCritical {
			return Built{}, fmt.Errorf("%w: instance %q must be %q or %q", ErrInvalidParams, inst, dbInstanceWarning, dbInstanceCritical)
		}
		if inst == dbInstanceCritical && p.DurationSeconds != nil && *p.DurationSeconds == 600 && p.Severity == SevWarning {
			p.DurationSeconds, p.Severity = dbI(7200), SevCritical
		}
		if err := dbValidate(p, false); err != nil {
			return Built{}, err
		}
		ids, err := dbUptimeIDs(p, ctx)
		if err != nil {
			return Built{}, err
		}
		dur := *p.DurationSeconds
		quoted := make([]string, len(ids))
		for i, id := range ids {
			quoted[i] = fmt.Sprintf("%q", id)
		}
		filter := fmt.Sprintf(`metric.type = %q AND resource.type = "uptime_url" AND metric.labels.check_id = one_of(%s)`, dbMetricUptime, strings.Join(quoted, ", "))
		agg1 := dbMap{"alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_NEXT_OLDER",
			"crossSeriesReducer": "REDUCE_COUNT_FALSE", "groupByFields": []string{"metric.label.check_id"}}
		cond := dbMap{
			"displayName": fmt.Sprintf("Uptime check failing for %s", dbHumanDuration(dur)),
			"conditionThreshold": dbMap{
				"filter":         filter,
				"aggregations":   []any{agg1},
				"comparison":     "COMPARISON_GT",
				"thresholdValue": 1,
				"duration":       fmt.Sprintf("%ds", dur),
				"trigger":        dbMap{"count": 1},
			},
		}
		level := map[string]string{dbInstanceWarning: "early warning", dbInstanceCritical: "prolonged outage"}[inst]
		doc := fmt.Sprintf("**Service down (%s).** An uptime check has been failing for %s (%d check(s) covered).\n\n"+
			"What to check:\n- The endpoint from outside (curl the health URL) and the service/VM behind it.\n- Load balancer / DNS / TLS certificate state.\n- Recent deploys.\n\n"+
			"This is the %s policy; cloud0 runs a \"warning\" (10 min) and a \"critical\" (2 h) policy per project.\n\n"+
			"Managed by cloud0 (template `%s`, instance `%s`); edit it from the Alerts tab, not in the console.",
			level, dbHumanDuration(dur), len(ids), inst, TplUptimeDown, inst)
		name := fmt.Sprintf("cloud0: %s (%s)", t.Title, dbHumanDuration(dur))
		raw, err := dbPolicy(name, doc, p, ctx, TplUptimeDown, inst, cond)
		return Built{Policy: raw}, err
	}}
}

// dbUptimeIDs resolves the check ids the policy covers: params.UptimeCheckIDs
// (validated against ctx.UptimeChecks when those are known) else every check
// of the project.
func dbUptimeIDs(p TemplateParams, ctx BuildContext) ([]string, error) {
	last := func(s string) string {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	known := map[string]bool{}
	var all []string
	for _, c := range ctx.UptimeChecks {
		if c.Project != "" && ctx.Project != "" && c.Project != ctx.Project {
			continue
		}
		id := last(c.Name)
		if id != "" && !known[id] {
			known[id] = true
			all = append(all, id)
		}
	}
	var ids []string
	seen := map[string]bool{}
	for _, raw := range p.UptimeCheckIDs {
		id := last(strings.TrimSpace(raw))
		if id == "" || seen[id] {
			continue
		}
		if len(known) > 0 && !known[id] {
			return nil, fmt.Errorf("%w: uptime check %q not found in project", ErrInvalidParams, id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ids = all
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: no uptime checks in project", ErrInvalidParams)
	}
	return ids, nil
}
