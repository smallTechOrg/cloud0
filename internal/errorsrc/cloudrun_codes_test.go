package errorsrc

import (
	"context"
	"testing"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

func crCodeSeries(service, class, code string, v int64) *monitoringpb.TimeSeries {
	ts := crSeries(service, class, v)
	ts.Metric.Labels["response_code"] = code
	return ts
}

func TestFetchCloudRun_ByCodeSumsEqualClassTotals(t *testing.T) {
	f := &fakeCR{series: map[string][]*monitoringpb.TimeSeries{"p1": {
		crCodeSeries("web", "2xx", "200", 900),
		crCodeSeries("web", "4xx", "404", 1463),
		crCodeSeries("web", "4xx", "401", 14),
		crCodeSeries("web", "5xx", "502", 2),
		crCodeSeries("api", "4xx", "404", 10),
		crCodeSeries("api", "5xx", "500", 3),
		crCodeSeries("api", "5xx", "503", 4),
		crCodeSeries("api", "4xx", "", 5), // class without a code label: class total only
	}}}
	res := FetchCloudRun(context.Background(), f, []string{"p1"}, crW42, crClock)
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("status %q %s", res.Status, res.Message)
	}
	byCode := map[string]map[string]int64{}
	for _, r := range res.Errors {
		byCode[r.Service] = r.ByCode
		sum := map[byte]int64{}
		for code, n := range r.ByCode {
			sum[code[0]] += n
		}
		if r.Service == "api" || r.Service == "" {
			// 5 unlabelled 4xx are in C4xx but in no code bucket.
			if want := map[string]int64{"api": 15, "": 1492}[r.Service]; *r.C4xx != want || sum['4'] != want-5 {
				t.Fatalf("api 4xx class=%d codes=%d", *r.C4xx, sum['4'])
			}
			continue
		}
		if sum['2'] != *r.C2xx || sum['4'] != *r.C4xx || sum['5'] != *r.C5xx {
			t.Fatalf("%q: code sums %v != classes %d/%d/%d", r.Service, sum, *r.C2xx, *r.C4xx, *r.C5xx)
		}
	}
	if byCode["web"]["404"] != 1463 || byCode["web"]["401"] != 14 || byCode["web"]["502"] != 2 {
		t.Fatalf("web by_code = %v", byCode["web"])
	}
	if tot := byCode[""]; tot["404"] != 1473 || tot["500"] != 3 || tot["503"] != 4 || tot["200"] != 900 {
		t.Fatalf("project total by_code = %v", tot)
	}
}

func TestFetchCloudRun_NoCodeLabelLeavesByCodeNil(t *testing.T) {
	f := &fakeCR{series: map[string][]*monitoringpb.TimeSeries{"p1": {crSeries("web", "4xx", 5)}}}
	res := FetchCloudRun(context.Background(), f, []string{"p1"}, crW42, crClock)
	for _, r := range res.Errors {
		if r.ByCode != nil {
			t.Fatalf("ByCode = %v, want nil", r.ByCode)
		}
	}
}
