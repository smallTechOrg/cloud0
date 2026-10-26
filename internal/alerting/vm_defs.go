package alerting

import (
	"encoding/json"
	"fmt"
)

// vmSpec is the table row behind one VM template.
type vmSpec struct {
	id, title, description, short string
	severity                      string
	threshold                     float64
	duration                      int
	metric                        string // primary metric type
	stateUsed                     bool   // add metric.labels.state="used"
	ratio                         bool   // threshold is a percent converted to a 0–1 ratio
	checkFirst                    string // markdown on-call guidance
	extraDisk                     bool   // add the bytes_used ratio condition (disk only)
	unitLabel                     string
}

func f64(v float64) *float64 { return &v }

func vmParamSpecs(label string) []ParamSpec {
	return []ParamSpec{
		{Key: "threshold", Label: label, Type: "number", Unit: "%", Min: f64(1), Max: f64(100), Options: []string{}, Help: ""},
		{Key: "duration_seconds", Label: "For", Type: "int", Unit: "seconds", Min: f64(60), Max: f64(86400), Options: []string{}, Help: "Condition must hold this long before alerting."},
		{Key: "severity", Label: "Severity", Type: "enum", Unit: "", Options: append([]string{}, Severities...), Help: ""},
	}
}

func vmSpecs() []vmSpec {
	return []vmSpec{
		{
			id: TplVMCPUHigh, title: "VM CPU high", short: "VM CPU high", unitLabel: "CPU",
			description: "CPU utilisation above the threshold on any Compute Engine VM, including future ones.",
			severity:    SevWarning, threshold: 80, duration: 300,
			metric: "compute.googleapis.com/instance/cpu/utilization", ratio: true,
			checkFirst: "1. Open the VM in Compute Engine > Observability and look at the CPU graph.\n2. SSH in and run `top` / `htop` to find the busy process.\n3. Check for a recent deploy, runaway job or traffic spike; resize the VM if the load is legitimate.",
		},
		{
			id: TplVMMemoryHigh, title: "VM memory high", short: "VM memory high", unitLabel: "Memory",
			description: "Memory used above the threshold on any VM running the Ops Agent, including future ones. VMs without the Ops Agent report no memory metric and are silently skipped.",
			severity:    SevError, threshold: 85, duration: 900,
			metric: "agent.googleapis.com/memory/percent_used", stateUsed: true,
			checkFirst: "1. SSH in and run `free -m` and `ps aux --sort=-%mem | head`.\n2. Look for a leaking process or a recent deploy; restart it if safe.\n3. Resize the VM or add swap if usage is legitimate.\n\nNote: needs the Ops Agent; VMs without it are not covered.",
		},
		{
			id: TplVMDiskHigh, title: "VM disk high", short: "VM disk high", unitLabel: "Disk",
			description: "Disk used above the threshold on any VM running the Ops Agent, including future ones. VMs without the Ops Agent are silently skipped.",
			severity:    SevError, threshold: 85, duration: 300,
			metric: "agent.googleapis.com/disk/percent_used", stateUsed: true, extraDisk: true,
			checkFirst: "1. SSH in and run `df -h` then `du -xh / --max-depth=2 | sort -h | tail`.\n2. Look for logs, docker images/volumes, or temp files; clean up or grow the disk.\n\nLimitation: the second condition (bytes used / bytes of all states) is summed across every device of the VM (it cannot be restricted to the root device), so a small full volume can be diluted by large empty ones; the first condition (percent used) is evaluated per device.",
		},
	}
}

func vmDefs() []templateDef {
	var out []templateDef
	for _, s := range vmSpecs() {
		s := s
		out = append(out, templateDef{
			Template: Template{
				ID: s.id, Kind: KindThreshold, Title: s.title, Description: s.description,
				Severity:   s.severity,
				ParamSpecs: vmParamSpecs(s.unitLabel + " %"),
				Defaults: TemplateParams{
					Threshold: f64(s.threshold), DurationSeconds: intp(s.duration),
					Severity: s.severity, Enabled: boolp(true),
				},
				DefaultEnabled: true,
			},
			Build: func(p TemplateParams, ctx BuildContext) (Built, error) { return buildVM(s, p, ctx) },
		})
	}
	return out
}

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

func buildVM(s vmSpec, p TemplateParams, ctx BuildContext) (Built, error) {
	if p.Threshold == nil || p.DurationSeconds == nil {
		return Built{}, fmt.Errorf("%w: threshold and duration_seconds are required", ErrInvalidParams)
	}
	thr := *p.Threshold
	if s.ratio {
		thr = thr / 100
	}
	sev := p.Severity
	if sev == "" {
		sev = s.severity
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	dur := fmt.Sprintf("%ds", *p.DurationSeconds)
	byInstance := []string{"resource.label.instance_id"}
	filter := fmt.Sprintf(`metric.type="%s" AND resource.type="gce_instance"`, s.metric)
	if s.stateUsed {
		filter += ` AND metric.labels.state="used"`
	}
	perInstance := []any{map[string]any{
		"alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_MEAN",
		"crossSeriesReducer": "REDUCE_NONE", "groupByFields": byInstance,
	}}
	conds := []any{map[string]any{
		"displayName": fmt.Sprintf("%s above %s%%", s.short, trimFloat(*p.Threshold)),
		"conditionThreshold": map[string]any{
			"filter": filter, "aggregations": perInstance,
			"comparison": "COMPARISON_GT", "thresholdValue": thr,
			"duration": dur, "trigger": map[string]any{"count": 1},
		},
	}}
	if s.extraDisk {
		num := `metric.type="agent.googleapis.com/disk/bytes_used" AND resource.type="gce_instance" AND metric.labels.state="used"`
		den := `metric.type="agent.googleapis.com/disk/bytes_used" AND resource.type="gce_instance" AND metric.labels.state!=""`
		sum := []any{map[string]any{
			"alignmentPeriod": "60s", "perSeriesAligner": "ALIGN_MEAN",
			"crossSeriesReducer": "REDUCE_SUM", "groupByFields": byInstance,
		}}
		conds = append(conds, map[string]any{
			"displayName": fmt.Sprintf("%s: bytes used ratio above %s%%", s.short, trimFloat(*p.Threshold)),
			"conditionThreshold": map[string]any{
				"filter": num, "aggregations": sum,
				"denominatorFilter": den, "denominatorAggregations": sum,
				"comparison": "COMPARISON_GT", "thresholdValue": *p.Threshold / 100,
				"duration": dur, "trigger": map[string]any{"count": 1},
			},
		})
	}
	channels := ctx.Channels
	if channels == nil {
		channels = []string{}
	}
	doc := fmt.Sprintf("**%s** on VM `${resource.label.instance_id}` (project `${project}`): %s above %s%% for %d s.\n\nWhat to check first:\n\n%s\n",
		s.short, s.unitLabel, trimFloat(*p.Threshold), *p.DurationSeconds, s.checkFirst)
	body := map[string]any{
		"displayName": "cloud0: " + s.short,
		"documentation": map[string]any{
			"content": doc, "mimeType": "text/markdown",
			"subject": s.short + ": ${resource.label.instance_id}",
		},
		"userLabels":           map[string]any{LabelManagedBy: LabelManagedByVal, LabelTemplate: s.id},
		"conditions":           conds,
		"combiner":             "OR",
		"enabled":              enabled,
		"notificationChannels": channels,
		"severity":             sev,
	}
	// Update bodies never carry `name`; ctx.Existing only selects update vs create at the caller.
	raw, err := json.Marshal(body)
	if err != nil {
		return Built{}, err
	}
	var w []string
	if len(channels) == 0 {
		w = append(w, "no notification channels: the policy will fire without notifying anyone")
	}
	return Built{Policy: raw, Warnings: w}, nil
}

func trimFloat(v float64) string { return fmt.Sprintf("%g", v) }
