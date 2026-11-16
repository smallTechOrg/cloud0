package refresh

import (
	"testing"

	"github.com/smallTechOrg/cloud0/internal/billing"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

func TestGroupRows_MergesDatesIntoOneWeeklyRowAndKeepsTheDailySplit(t *testing.T) {
	w := isoweek.MustParse("2026-W42")
	vm := "vm-a"
	row := func(date string, gross, credits float64) billing.Row {
		return billing.Row{ISOYear: 2026, ISOWeek: 42, ProjectID: "p", Service: "Compute Engine", SKU: "Core", ResourceName: &vm,
			Gross: gross, Credits: credits, Net: gross + credits, LineItems: 2, UsageDate: date}
	}
	other := row("2026-10-13", 5, 0)
	other.SKU = "RAM"
	nodate := row("", 1, 0)
	nodate.SKU = "Legacy"

	weekly, daily := (&Runner{}).groupRows(billing.WeekWindow(w, 1), []billing.Row{
		row("2026-10-12", 10, -1), row("2026-10-13", 20, -2), other, nodate,
	})
	if len(weekly[w]) != 3 {
		t.Fatalf("weekly rows = %+v", weekly[w])
	}
	core := weekly[w][0]
	if core.SKU != "Core" || core.Gross != 30 || core.Credits != -3 || core.Net != 27 || core.LineItems != 4 {
		t.Errorf("merged row = %+v", core)
	}
	if len(daily[w]) != 3 || daily[w][0].UsageDate != "2026-10-12" || daily[w][1].Net != 18 || daily[w][2].SKU != "RAM" {
		t.Errorf("daily rows = %+v", daily[w])
	}
	var wsum, dsum float64
	for _, r := range weekly[w] {
		if r.SKU != "Legacy" {
			wsum += r.Net
		}
	}
	for _, r := range daily[w] {
		dsum += r.Net
	}
	if wsum != dsum {
		t.Errorf("Σ weekly %v ≠ Σ daily %v", wsum, dsum)
	}
}
