package httpapi

// registerAlertRoutes wires the Alerts endpoints of spec/api.md. Read-only
// endpoints live in alerts_read.go, preview/apply/adopt in alerts_write.go;
// two workers own them separately.
func (d Deps) registerAlertRoutes(r *router) {
	d.registerAlertsRead(r)
	d.registerAlertsWrite(r)
}
