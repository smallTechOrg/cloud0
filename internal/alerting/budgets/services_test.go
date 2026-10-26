package budgets

import (
	"context"
	"errors"
	"testing"
)

type fakeLister struct {
	s   []Service
	err error
}

func (f fakeLister) ListServices(context.Context) ([]Service, error) { return f.s, f.err }

func TestServiceIDs(t *testing.T) {
	l := fakeLister{s: []Service{{ID: "6F81-5844-456A", Name: "Compute Engine"}, {ID: "services/152E-C115-5142", Name: "Cloud Run"}}}
	ids, missing, err := ServiceIDs(context.Background(), l, "compute engine", "Cloud Run", "Cloud SQL")
	if err != nil {
		t.Fatal(err)
	}
	if ids["compute engine"] != "services/6F81-5844-456A" || ids["Cloud Run"] != "services/152E-C115-5142" || len(missing) != 1 || missing[0] != "Cloud SQL" {
		t.Errorf("ids=%v missing=%v", ids, missing)
	}
	boom := errors.New("boom")
	if _, _, err := ServiceIDs(context.Background(), fakeLister{err: boom}, "x"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestNewBQServiceLister_RejectsBadTable(t *testing.T) {
	if _, err := NewBQServiceLister(nil, "p.d.t`; DROP", nil); err == nil {
		t.Error("bad table accepted")
	}
}

func TestServiceQueryIsPartitionFiltered(t *testing.T) {
	q := ServiceQuery("p.d.t")
	for _, w := range []string{"_PARTITIONTIME", "@part_start", "@usage_start", "`p.d.t`"} {
		if !contains(q, w) {
			t.Errorf("query lacks %s", w)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
