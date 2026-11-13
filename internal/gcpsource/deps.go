package gcpsource

// Pins the Phase 2 client libraries in go.mod so parallel source slices never
// edit go.mod. Remove once every package below is imported for real.
import (
	_ "cloud.google.com/go/errorreporting/apiv1beta1"
	_ "cloud.google.com/go/logging/logadmin"
	_ "cloud.google.com/go/monitoring/apiv3/v2"
	_ "cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
)
