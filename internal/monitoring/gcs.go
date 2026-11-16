package monitoring

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// SourceGCS is the run_sources id of the Cloud Storage family.
const SourceGCS = "monitoring_gcs"

// Cloud Storage metric types read by FetchGCS.
const (
	gcsRequestCountMetric = "storage.googleapis.com/api/request_count"
	gcsSentBytesMetric    = "storage.googleapis.com/network/sent_bytes_count"
	gcsTotalBytesMetric   = "storage.googleapis.com/storage/total_bytes"
)

// GCS operation classes (values of GCSOperationClass).
const (
	GCSClassA    = "A"
	GCSClassB    = "B"
	GCSClassFree = "free"
)

// HoursPerMonth is the month length Google uses for GB-month storage pricing.
const HoursPerMonth = 730.0

// GCSOperationClass maps the `method` label of
// storage.googleapis.com/api/request_count to Google's published Cloud
// Storage operation class (cloud.google.com/storage/pricing#operations-by-class):
//
//   - Class A: inserts/writes, patches/updates, setIamPolicy, bucket and
//     object listing, compose, copy, rewrite, restore, watchAll,
//     retention-policy lock, HMAC key create/list/update, notification
//     create/delete, ACL insert/update/patch/delete.
//   - Class B: gets (object reads and metadata, bucket metadata),
//     getIamPolicy, testIamPermissions, ACL list/get, notification
//     list/get, HMAC key get, getServiceAccount.
//   - Free: object/bucket/HMAC deletes, channels.stop.
//
// A method absent from this map is counted in neither class and named in
// Result.Message so the mapping can be extended.
var GCSOperationClass = map[string]string{
	// Class A — objects
	"WriteObject":          GCSClassA,
	"BidiWriteObject":      GCSClassA,
	"StartResumableWrite":  GCSClassA,
	"QueryWriteStatus":     GCSClassA,
	"InsertObject":         GCSClassA,
	"ComposeObject":        GCSClassA,
	"CopyObject":           GCSClassA,
	"RewriteObject":        GCSClassA,
	"ListObjects":          GCSClassA,
	"UpdateObject":         GCSClassA,
	"UpdateObjectMetadata": GCSClassA,
	"PatchObject":          GCSClassA,
	"PatchObjectMetadata":  GCSClassA,
	"RestoreObject":        GCSClassA,
	"BulkRestoreObjects":   GCSClassA,
	"WatchAllObjects":      GCSClassA,
	"SetObjectIamPolicy":   GCSClassA,
	// Class A — buckets
	"CreateBucket":                     GCSClassA,
	"InsertBucket":                     GCSClassA,
	"ListBuckets":                      GCSClassA,
	"UpdateBucket":                     GCSClassA,
	"UpdateBucketMetadata":             GCSClassA,
	"PatchBucket":                      GCSClassA,
	"PatchBucketMetadata":              GCSClassA,
	"SetBucketIamPolicy":               GCSClassA,
	"SetIamPolicy":                     GCSClassA,
	"LockBucketRetentionPolicy":        GCSClassA,
	"LockRetentionPolicy":              GCSClassA,
	"CreateNotification":               GCSClassA,
	"CreateNotificationConfig":         GCSClassA,
	"DeleteNotification":               GCSClassA,
	"DeleteNotificationConfig":         GCSClassA,
	"CreateHmacKey":                    GCSClassA,
	"ListHmacKeys":                     GCSClassA,
	"UpdateHmacKey":                    GCSClassA,
	"InsertBucketAccessControl":        GCSClassA,
	"UpdateBucketAccessControl":        GCSClassA,
	"PatchBucketAccessControl":         GCSClassA,
	"DeleteBucketAccessControl":        GCSClassA,
	"InsertObjectAccessControl":        GCSClassA,
	"UpdateObjectAccessControl":        GCSClassA,
	"PatchObjectAccessControl":         GCSClassA,
	"DeleteObjectAccessControl":        GCSClassA,
	"InsertDefaultObjectAccessControl": GCSClassA,
	"UpdateDefaultObjectAccessControl": GCSClassA,
	"PatchDefaultObjectAccessControl":  GCSClassA,
	"DeleteDefaultObjectAccessControl": GCSClassA,
	// Class B
	"ReadObject":                      GCSClassB,
	"BidiReadObject":                  GCSClassB,
	"GetObject":                       GCSClassB,
	"GetObjectMetadata":               GCSClassB,
	"GetObjectIamPolicy":              GCSClassB,
	"TestObjectIamPermissions":        GCSClassB,
	"GetBucket":                       GCSClassB,
	"GetBucketMetadata":               GCSClassB,
	"GetBucketIamPolicy":              GCSClassB,
	"GetIamPolicy":                    GCSClassB,
	"TestBucketIamPermissions":        GCSClassB,
	"TestIamPermissions":              GCSClassB,
	"GetNotification":                 GCSClassB,
	"GetNotificationConfig":           GCSClassB,
	"ListNotifications":               GCSClassB,
	"ListNotificationConfigs":         GCSClassB,
	"GetHmacKey":                      GCSClassB,
	"GetServiceAccount":               GCSClassB,
	"ListBucketAccessControls":        GCSClassB,
	"GetBucketAccessControl":          GCSClassB,
	"ListObjectAccessControls":        GCSClassB,
	"GetObjectAccessControl":          GCSClassB,
	"ListDefaultObjectAccessControls": GCSClassB,
	"GetDefaultObjectAccessControl":   GCSClassB,
	// Free
	"DeleteObject":  GCSClassFree,
	"DeleteBucket":  GCSClassFree,
	"DeleteHmacKey": GCSClassFree,
	"StopChannel":   GCSClassFree,
}

// gcsBucket accumulates one bucket's figures; has* distinguish "no series"
// (a gap) from a measured zero.
type gcsBucket struct {
	classA, classB, sentBytes, meanBytes float64
	hasReq, hasSent, hasStorage          bool
}

// gcsFamily is one of the three queries FetchGCS runs per project.
type gcsFamily struct {
	query Query
	prov  gcpsource.Provenance
	err   error
	ts    []*monitoringpb.TimeSeries
}

// FetchGCS reads the Cloud Storage block for window w: one row per bucket
// (Group gcs, Resource = bucket name) plus a per-project total row
// (Resource ""). Cells:
//
//   - class_a_ops / class_b_ops: api/request_count, ALIGN_SUM over the
//     window, summed per bucket and split by the `method` label via
//     GCSOperationClass (unit "count").
//   - sent_gb: network/sent_bytes_count, ALIGN_SUM, bytes / 1e9 (unit "GB").
//   - storage_gb_month: storage/total_bytes, ALIGN_MEAN over the window
//     (summed across storage classes), then
//     mean_bytes / 1e9 × (window hours / 730) (unit "GB-month") — the
//     window's share of a 730-hour pricing month.
//
// A failing query yields "source failed" cells for that metric in that
// project while the rest still fills in; the Result status is the
// classification of the first error and Message names every failure.
func FetchGCS(ctx context.Context, l Lister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	r := gcpsource.Result{Source: SourceGCS, Started: now().UTC()}
	var failures []glProjectErr
	unknown := map[string]float64{}

	for _, p := range glCleanProjects(projects) {
		fetched := now().UTC()
		fams := []*gcsFamily{
			{query: Query{Project: p, Filter: glMetricFilter(gcsRequestCountMetric), Window: w,
				Aligner: monitoringpb.Aggregation_ALIGN_SUM, Reducer: monitoringpb.Aggregation_REDUCE_SUM,
				GroupBy: []string{"resource.labels.bucket_name", "metric.labels.method"}}},
			{query: Query{Project: p, Filter: glMetricFilter(gcsSentBytesMetric), Window: w,
				Aligner: monitoringpb.Aggregation_ALIGN_SUM, Reducer: monitoringpb.Aggregation_REDUCE_SUM,
				GroupBy: []string{"resource.labels.bucket_name"}}},
			{query: Query{Project: p, Filter: glMetricFilter(gcsTotalBytesMetric), Window: w,
				Aligner: monitoringpb.Aggregation_ALIGN_MEAN, Reducer: monitoringpb.Aggregation_REDUCE_SUM,
				GroupBy: []string{"resource.labels.bucket_name"}}},
		}
		for _, f := range fams {
			f.prov = f.query.Provenance(SourceGCS, fetched)
			r.Provenance = append(r.Provenance, f.prov)
			f.ts, f.err = f.query.Run(ctx, l)
			if f.err != nil {
				failures = append(failures, glProjectErr{project: p, metric: f.query.Filter, err: f.err})
			}
		}
		req, sent, stor := fams[0], fams[1], fams[2]

		buckets := map[string]*gcsBucket{}
		get := func(name string) *gcsBucket {
			b, ok := buckets[name]
			if !ok {
				b = &gcsBucket{}
				buckets[name] = b
			}
			return b
		}
		for _, ts := range req.ts {
			b := get(Label(ts, "bucket_name"))
			b.hasReq = true
			method := Label(ts, "method")
			if method == "" {
				method = Label(ts, "method_name")
			}
			v := Value(ts)
			switch GCSOperationClass[method] {
			case GCSClassA:
				b.classA += v
			case GCSClassB:
				b.classB += v
			case GCSClassFree:
			default:
				unknown[method] += v
			}
		}
		for _, ts := range sent.ts {
			b := get(Label(ts, "bucket_name"))
			b.hasSent = true
			b.sentBytes += Value(ts)
		}
		for _, ts := range stor.ts {
			b := get(Label(ts, "bucket_name"))
			b.hasStorage = true
			b.meanBytes += Value(ts)
		}

		names := make([]string, 0, len(buckets))
		for n := range buckets {
			names = append(names, n)
		}
		sort.Strings(names)
		total := gcsBucket{}
		monthShare := w.Hours() / HoursPerMonth
		for _, n := range names {
			b := buckets[n]
			cells := map[string]gcpsource.Cell{}
			cells[gcpsource.MClassAOps] = gcsCell(req, b.hasReq, b.classA, "count", "no requests recorded for this bucket in the window")
			cells[gcpsource.MClassBOps] = gcsCell(req, b.hasReq, b.classB, "count", "no requests recorded for this bucket in the window")
			cells[gcpsource.MSentGB] = gcsCell(sent, b.hasSent, b.sentBytes/1e9, "GB", "no egress recorded for this bucket in the window")
			cells[gcpsource.MStorageGBMonth] = gcsCell(stor, b.hasStorage, b.meanBytes/1e9*monthShare, "GB-month", "no storage samples for this bucket in the window")
			r.Metrics = append(r.Metrics, gcpsource.MetricRow{Group: gcpsource.GroupGCS, Project: p, Resource: n, Cells: cells})
			total.classA += b.classA
			total.classB += b.classB
			total.sentBytes += b.sentBytes
			total.meanBytes += b.meanBytes
			total.hasReq = total.hasReq || b.hasReq
			total.hasSent = total.hasSent || b.hasSent
			total.hasStorage = total.hasStorage || b.hasStorage
		}
		r.Metrics = append(r.Metrics, gcpsource.MetricRow{Group: gcpsource.GroupGCS, Project: p, Resource: "", Cells: map[string]gcpsource.Cell{
			gcpsource.MClassAOps:      gcsCell(req, total.hasReq, total.classA, "count", "no Cloud Storage requests recorded in the window"),
			gcpsource.MClassBOps:      gcsCell(req, total.hasReq, total.classB, "count", "no Cloud Storage requests recorded in the window"),
			gcpsource.MSentGB:         gcsCell(sent, total.hasSent, total.sentBytes/1e9, "GB", "no Cloud Storage egress recorded in the window"),
			gcpsource.MStorageGBMonth: gcsCell(stor, total.hasStorage, total.meanBytes/1e9*monthShare, "GB-month", "no Cloud Storage buckets with data in the window"),
		}})
	}

	glFinishWithProjectErrors(&r, now(), failures)
	if len(unknown) > 0 {
		r.Message = glJoinNonEmpty(r.Message, "unclassified GCS methods (counted in neither Class A nor B): "+glFormatCounts(unknown))
	}
	return r
}

// gcsCell builds a value cell, a "source failed" gap when the family's query
// failed, or a "no data" gap when the query returned no series for it.
func gcsCell(f *gcsFamily, has bool, v float64, unit, noDataHint string) gcpsource.Cell {
	if f.err != nil {
		_, _, hint := gcpsource.Classify(f.err, RoleHint, API)
		return gcpsource.G("source failed", hint, unit, f.prov)
	}
	if !has {
		return gcpsource.G("no data", noDataHint, unit, f.prov)
	}
	return gcpsource.V(v, unit, f.prov)
}

// glProjectErr is one failed query, kept so the Result can name all of them.
type glProjectErr struct {
	project, metric string
	err             error
}

// glFinishWithProjectErrors stamps r: ok when nothing failed, otherwise the
// classification of the first error with a Message naming every failure.
// Rows already gathered from successful queries are kept (partial data).
func glFinishWithProjectErrors(r *gcpsource.Result, now time.Time, failures []glProjectErr) {
	if len(failures) == 0 {
		r.Finish(now.UTC(), nil, RoleHint, API)
		return
	}
	r.Finish(now.UTC(), failures[0].err, RoleHint, API)
	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		parts = append(parts, fmt.Sprintf("project %s, %s: %v", f.project, f.metric, f.err))
	}
	r.Message = strings.Join(parts, "; ")
}

func glMetricFilter(metricType string) string { return `metric.type="` + metricType + `"` }

// glCleanProjects trims ids and drops empties and duplicates, keeping order.
func glCleanProjects(projects []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range projects {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func glFormatCounts(m map[string]float64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		name := k
		if name == "" {
			name = "(no method label)"
		}
		parts = append(parts, fmt.Sprintf("%s=%.0f", name, m[k]))
	}
	return strings.Join(parts, ", ")
}

func glJoinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
