package attribution

import "testing"

func resRow(project, svc, sku, name, global string, net float64, labels map[string]string) CostRow {
	return CostRow{Row: Row{ProjectID: project, Service: svc, SKU: sku, ResourceName: name, ResourceGlobalName: global, Labels: labels}, Net: net}
}

func TestBuildResources_VMAndDiskShareOneGroupWithTwoTargets(t *testing.T) {
	rules := []Rule{{Priority: 50, Match: Match{Field: FieldResourceName, Op: OpEquals, Value: "web-vm"}, Action: Action{Product: "web", Environment: "prod"}, Enabled: true, Source: "user"}}
	eng, err := NewEngine(rules)
	if err != nil {
		t.Fatal(err)
	}
	rows := []CostRow{
		resRow("p", "Compute Engine", "N1 Core", "projects/123/instances/web-vm", "//compute.googleapis.com/projects/123/zones/zone-a/instances/999", 70, nil),
		resRow("p", "Compute Engine", "PD Capacity", "web-vm", "//compute.googleapis.com/projects/123/zones/zone-a/disk/555", 30, nil),
		resRow("p", "Networking", "LB Rule", "", "", 20, nil),
		resRow("p", "Cloud Run", "CPU", "api", "//run.googleapis.com/projects/p/locations/region-a/services/api", 10, map[string]string{"product": "core"}),
	}
	groups, products, _, total := BuildResources(rows, eng, nil)
	if total != 130 || len(groups) != 3 {
		t.Fatalf("total=%v groups=%d", total, len(groups))
	}
	vm := groups[0]
	if vm.Name != "web-vm" || vm.Net != 100 || vm.Product != "web" || vm.Env != "prod" || vm.Source != SourceRule {
		t.Errorf("vm group = %+v", vm)
	}
	if vm.Match.Field != FieldResourceName || vm.Match.Value != "web-vm" || len(vm.Members) != 2 {
		t.Errorf("match/members: %+v", vm)
	}
	types := map[string]string{}
	for _, tg := range vm.Targets {
		types[tg.Type] = tg.Location
		if !tg.Writable || tg.Name != "web-vm" {
			t.Errorf("target %+v", tg)
		}
	}
	if types[TypeComputeInstance] != "zone-a" || types[TypeComputeDisk] != "zone-a" {
		t.Errorf("targets = %v", types)
	}
	var api, lb *ResourceGroup
	for i := range groups {
		switch groups[i].Name {
		case "api":
			api = &groups[i]
		default:
			if groups[i].Kind == "sku" {
				lb = &groups[i]
			}
		}
	}
	if api == nil || api.Source != SourceLabel || api.Product != "core" {
		t.Errorf("label-decided group = %+v", api)
	}
	if lb == nil || lb.Source != SourceNone || lb.Match.Field != FieldSKU || len(lb.Targets) != 0 {
		t.Errorf("sku group = %+v", lb)
	}
	if len(products) != 2 {
		t.Errorf("products = %+v", products)
	}
}

func TestLabelAddsAndCommand(t *testing.T) {
	l := LabelAdds(" My Product ", "Prod")
	if l["product"] != "my-product" || l["env"] != "prod" {
		t.Errorf("adds = %v", l)
	}
	cmd, risk := LabelCommand(TypeComputeDisk, "p", "d", "z", l)
	if cmd == nil || risk == "" {
		t.Fatal("no command")
	}
}
