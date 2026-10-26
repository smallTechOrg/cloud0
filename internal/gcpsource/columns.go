package gcpsource

// MetricColumn is one column of a Metrics-table group.
type MetricColumn struct {
	Key   string
	Label string
	Unit  string
}

// MetricGroupSpec is one group of the Metrics table with its ordered columns.
type MetricGroupSpec struct {
	Group   string
	Label   string
	Columns []MetricColumn
}

// ColErr4xx / ColErr5xx are the Errors-group columns of the Metrics table;
// their cells are derived from the Cloud Run ErrorRows, not from MetricRows.
const (
	ColErr4xx = "c4xx"
	ColErr5xx = "c5xx"
)

// MetricGroups is the frozen group and column order of the Metrics table
// (the sheet's Metrics-tab order, spec/api.md → Phase 2).
func MetricGroups() []MetricGroupSpec {
	util := func() []MetricColumn {
		return []MetricColumn{
			{MCPU, "CPU %", "%"},
			{MMemory, "Memory %", "%"},
			{MDisk, "Disk %", "%"},
		}
	}
	return []MetricGroupSpec{
		{GroupVM, "VM", util()},
		{GroupCloudSQL, "Cloud SQL", util()},
		{GroupNetworking, "Networking", []MetricColumn{
			{MLBHours, "LB hours", "hours"},
			{MOutboundGB, "Outbound GB", "GB"},
			{MInboundGB, "Inbound GB", "GB"},
		}},
		{GroupCloudRun, "Cloud Run", []MetricColumn{
			{MServiceMemGiBs, "Memory (GiB-s)", "GiB-s"},
			{MRequests, "Requests", "count"},
			{MServiceCPUSec, "CPU (vCPU-s)", "vCPU-s"},
			{MEgressInterGB, "Egress intercontinental GB", "GB"},
		}},
		{GroupGCS, "Cloud Storage", []MetricColumn{
			{MClassAOps, "Class A ops", "count"},
			{MClassBOps, "Class B ops", "count"},
			{MDownloadAPACGB, "Download APAC GB", "GB"},
			{MSentGB, "Sent GB", "GB"},
			{MStorageGBMonth, "Storage GB-month", "GB-month"},
		}},
		{GroupLogging, "Cloud Logging", []MetricColumn{
			{MLoggingGB, "Ingested GB", "GB"},
		}},
		{GroupErrors, "Errors", []MetricColumn{
			{ColErr4xx, "4xx", "count"},
			{ColErr5xx, "5xx", "count"},
		}},
	}
}
