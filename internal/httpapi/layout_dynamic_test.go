package httpapi

import (
	"testing"

	"github.com/smallTechOrg/cloud0/internal/costtable"
)

func TestLimitAllowances_GroqRowsOnlyWhenGroqIsAVendorColumn(t *testing.T) {
	prev := costtable.ActiveLayout()
	t.Cleanup(func() { costtable.SetLayout(prev) })

	count := func() (groq int) {
		for _, a := range limitAllowances() {
			if a.Service == "Groq" {
				groq++
			}
		}
		return groq
	}
	costtable.SetLayout(costtable.DynamicLayout())
	if n := count(); n != 0 {
		t.Errorf("dynamic layout: %d Groq rows, want 0", n)
	}
	costtable.SetLayout(costtable.MustParseLayout("", costtable.SheetVendors))
	if n := count(); n != 4 {
		t.Errorf("groq vendor column: %d Groq rows, want 4", n)
	}
}
