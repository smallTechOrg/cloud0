package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/smallTechOrg/cloud0/internal/attribution"
)

// handleAttrExport is the portable view of the attribution: one row per
// resource group, named after FinOps FOCUS columns (SubAccountId, ResourceName,
// ServiceName, EffectiveCost, Tags) so any reporting tool can join it to its own
// billing data. Product/environment are plain `Tags` keys (the same `product`
// and `env` labels cloud0 writes to GCP) plus x_ extension columns.
//
//	GET /api/v1/attribution/export?week=&scope=&format=json|csv
func (d Deps) handleAttrExport(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	format := strings.ToLower(q.Get("format"))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		return ErrInvalidRequest("format must be json or csv.")
	}
	ac, aerr := d.attrLoad(r.Context(), ad, q.Get("week"), q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	type row struct {
		SubAccountID      string            `json:"SubAccountId"`
		ResourceName      string            `json:"ResourceName"`
		ServiceName       string            `json:"ServiceName"`
		EffectiveCost     float64           `json:"EffectiveCost"`
		Currency          string            `json:"BillingCurrency"`
		Tags              map[string]string `json:"Tags"`
		Product           string            `json:"x_Product"`
		Environment       string            `json:"x_Environment"`
		AttributionSource string            `json:"x_AttributionSource"`
		ChargePeriodStart string            `json:"ChargePeriodStart"`
		ChargePeriodEnd   string            `json:"ChargePeriodEnd"`
	}
	rows := []row{}
	if ac.snap != nil {
		groups, _, _, _ := attribution.BuildResources(ac.cost, ac.eng, nil)
		start, end := ac.week.Start().Format("2006-01-02"), ac.week.End().Format("2006-01-02")
		for _, g := range groups {
			tags := map[string]string{}
			for k, v := range g.Labels {
				tags[k] = v
			}
			if g.Product != "" {
				tags["product"] = g.Product
			}
			if g.Shared {
				tags["product"] = "shared"
			}
			if g.Env != "" {
				tags["env"] = g.Env
			}
			rows = append(rows, row{SubAccountID: g.ProjectID, ResourceName: g.Name, ServiceName: strings.Join(g.Services, "; "),
				EffectiveCost: g.Net, Currency: d.Config.Currency, Tags: tags, Product: g.Product, Environment: g.Env,
				AttributionSource: g.Source, ChargePeriodStart: start, ChargePeriodEnd: end})
		}
	}
	fname := "cloud0-attribution-" + ac.week.String()
	if format == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`.json"`)
		writeJSON(w, http.StatusOK, map[string]any{"week": ac.week.String(), "scope": ac.scope, "rows": rows})
		return nil
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ChargePeriodStart", "ChargePeriodEnd", "SubAccountId", "ResourceName", "ServiceName", "EffectiveCost", "BillingCurrency", "Tags", "x_Product", "x_Environment", "x_AttributionSource"})
	for _, x := range rows {
		tags, _ := json.Marshal(x.Tags)
		_ = cw.Write([]string{x.ChargePeriodStart, x.ChargePeriodEnd, x.SubAccountID, x.ResourceName, x.ServiceName,
			strconv.FormatFloat(x.EffectiveCost, 'f', 2, 64), x.Currency, string(tags), x.Product, x.Environment, x.AttributionSource})
	}
	cw.Flush()
	return nil
}
