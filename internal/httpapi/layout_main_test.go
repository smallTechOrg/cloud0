package httpapi

import (
	"os"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/costtable"
)

// TestMain installs the original sheet layout: most tests in this package
// assert on its fixed column keys. The dynamic default has its own tests,
// which install it explicitly.
func TestMain(m *testing.M) {
	costtable.SetLayout(costtable.MustParseLayout(costtable.SheetColumns, costtable.SheetVendors))
	os.Exit(m.Run())
}
