package attribution

import "testing"

// A compute global_name ends in the numeric instance id; the command must use
// the instance NAME from the billing resource name.
func TestComputeInstanceCommandUsesNameNotNumericID(t *testing.T) {
	ev := lsEval("example-product", "prod", lsID(2), lsID(3))
	row := lsRow("proj-a", "projects/123/instances/example-vm",
		"//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/4646638216258999502", 10)
	items, _ := lsSuggest(t, []CostRow{row}, map[string]Evaluation{"projects/123/instances/example-vm": ev}, nil)
	if len(items) != 1 || items[0].Command == nil {
		t.Fatalf("want one item with a command, got %+v", items)
	}
	want := "gcloud compute instances add-labels example-vm --project=proj-a --zone=zone-a --labels=product=example-product,env=prod"
	if *items[0].Command != want {
		t.Fatalf("command = %q, want %q", *items[0].Command, want)
	}
}
