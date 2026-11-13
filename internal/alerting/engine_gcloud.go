package alerting

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func engShq(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./_-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func engLastSeg(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// gcloud renders the equivalent CLI text of the planned write. Secrets are never included.
func (e *engine) gcloud(p *engPending) string {
	switch p.kind {
	case TargetPolicy:
		switch p.action {
		case ActionCreate:
			return e.d.Builder.Gcloud(p.project, "", p.policyBody)
		case ActionUpdate:
			return e.d.Builder.Gcloud(p.project, p.target, p.policyBody)
		case ActionDelete:
			return fmt.Sprintf("gcloud monitoring policies delete %s", engShq(p.target))
		case ActionEnable:
			return fmt.Sprintf("gcloud monitoring policies update %s --enabled", engShq(p.target))
		case ActionDisable:
			return fmt.Sprintf("gcloud monitoring policies update %s --no-enabled", engShq(p.target))
		}
	case TargetUptimeCheck:
		d := p.uptime
		flags := func() string {
			proto := "http"
			if d.UseSSL {
				proto = "https"
			}
			f := fmt.Sprintf("--protocol=%s --path=%s --port=%d --period=%d --timeout=%d", proto, engShq(d.Path), d.Port, d.PeriodSeconds/60, d.TimeoutSeconds)
			if d.ContentMatch != "" {
				f += " --matcher-content=" + engShq(d.ContentMatch)
			}
			return f
		}
		switch p.action {
		case ActionCreate:
			return fmt.Sprintf("gcloud monitoring uptime create %s --project=%s --resource-type=uptime-url --resource-labels=host=%s,project_id=%s %s",
				engShq(d.DisplayName), engShq(d.Project), engShq(d.Host), engShq(d.Project), flags())
		case ActionUpdate:
			return fmt.Sprintf("gcloud monitoring uptime update %s --project=%s --display-name=%s %s", engShq(engLastSeg(p.target)), engShq(p.project), engShq(d.DisplayName), flags())
		case ActionDelete:
			return fmt.Sprintf("gcloud monitoring uptime delete %s --project=%s", engShq(engLastSeg(p.target)), engShq(p.project))
		}
	case TargetBudget:
		acct := "BILLING_ACCOUNT_ID"
		if parts := strings.Split(p.target, "/"); len(parts) >= 2 && parts[0] == "billingAccounts" {
			acct = parts[1]
		}
		if p.action == ActionDelete {
			return fmt.Sprintf("gcloud billing budgets delete %s --billing-account=%s", engShq(engLastSeg(p.target)), engShq(acct))
		}
		d := p.budget
		var sb strings.Builder
		verb := "create"
		if p.action == ActionUpdate {
			verb = "update " + engShq(engLastSeg(p.target))
		}
		fmt.Fprintf(&sb, "gcloud billing budgets %s --billing-account=%s --display-name=%s --budget-amount=%g", verb, engShq(acct), engShq(d.DisplayName), d.Amount)
		for _, t := range d.Thresholds {
			basis := "current-spend"
			if t.Basis == "forecasted_spend" {
				basis = "forecasted-spend"
			}
			fmt.Fprintf(&sb, " --threshold-rule=percent=%g,basis=%s", t.Percent/100, basis)
		}
		if len(d.Projects) > 0 {
			ps := make([]string, len(d.Projects))
			for i, x := range d.Projects {
				ps[i] = "projects/" + strings.TrimPrefix(x, "projects/")
			}
			fmt.Fprintf(&sb, " --filter-projects=%s", engShq(strings.Join(ps, ",")))
		}
		if len(d.ServiceIDs) > 0 {
			fmt.Fprintf(&sb, " --filter-services=%s", engShq(strings.Join(d.ServiceIDs, ",")))
		}
		if len(p.budgetChans) > 0 {
			fmt.Fprintf(&sb, " --notifications-rule-monitoring-notification-channels=%s", engShq(strings.Join(p.budgetChans, ",")))
		}
		if d.DefaultIAMRecipients != nil && !*d.DefaultIAMRecipients {
			sb.WriteString(" --disable-default-iam-recipients")
		}
		return sb.String()
	case TargetSnooze:
		switch p.action {
		case ActionCreate:
			var v struct {
				Interval struct{ StartTime, EndTime string } `json:"interval"`
			}
			_ = json.Unmarshal(p.desired, &v)
			return fmt.Sprintf("gcloud monitoring snoozes create --project=%s --display-name=%s --criteria-policies=%s --start-time=%s --end-time=%s",
				engShq(p.snooze.Project), engShq(p.snooze.DisplayName), engShq(strings.Join(p.snooze.PolicyNames, ",")), v.Interval.StartTime, v.Interval.EndTime)
		case ActionEnd:
			var v struct {
				Interval struct{ EndTime string } `json:"interval"`
			}
			_ = json.Unmarshal(p.desired, &v)
			return fmt.Sprintf("gcloud monitoring snoozes update %s --end-time=%s", engShq(p.target), v.Interval.EndTime)
		}
	case TargetChannel:
		switch p.action {
		case ActionCreate:
			d := p.channel
			var labels []string
			for k, v := range d.Labels {
				labels = append(labels, k+"="+v)
			}
			sort.Strings(labels)
			s := fmt.Sprintf("gcloud beta monitoring channels create --project=%s --display-name=%s --type=%s", engShq(d.Project), engShq(d.DisplayName), engShq(d.Type))
			if len(labels) > 0 {
				s += " --channel-labels=" + engShq(strings.Join(labels, ","))
			}
			if len(d.SensitiveLabels) > 0 {
				s += "  # sensitive labels omitted: supply them in the console or via the API"
			}
			return s
		case ActionDelete:
			return fmt.Sprintf("gcloud beta monitoring channels delete %s", engShq(p.target))
		}
	}
	return ""
}
