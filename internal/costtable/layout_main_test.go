package costtable

import (
	"os"
	"testing"
)

// TestMain installs the original sheet layout: most tests in this package
// assert on its fixed column keys. The dynamic default has its own tests,
// which install it explicitly.
func TestMain(m *testing.M) {
	SetLayout(MustParseLayout(SheetColumns, SheetVendors))
	os.Exit(m.Run())
}
