package httpapi

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/smallTechOrg/cloud0/internal/errorsrc"
	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

type samplesResponse struct {
	Week        string    `json:"week"`
	Project     string    `json:"project"`
	Service     string    `json:"service"`
	MinStatus   int       `json:"min_status"`
	GeneratedAt time.Time `json:"generated_at"`
	LogsURL     string    `json:"logs_url"`
	errorsrc.SampleResult
}

// handleErrorSamples serves GET /errors/samples: an on-demand, capped sample of
// the week's failing Cloud Run requests from Cloud Logging.
func (d Deps) handleErrorSamples(w http.ResponseWriter, r *http.Request) *APIError {
	now := d.now()
	q := r.URL.Query()
	week, aerr := d.weekParam(q.Get("week"), now)
	if aerr != nil {
		return aerr
	}
	service := q.Get("service")
	if service == "" {
		return ErrInvalidRequest("The service query parameter is required.").WithHint("Pass the Cloud Run service name, e.g. ?service=my-service.")
	}
	project := q.Get("project")
	switch {
	case project == "" && len(d.Config.Projects) == 1:
		project = d.Config.Projects[0]
	case project == "":
		return ErrInvalidRequest("The project query parameter is required when several projects are configured.").
			WithHint("Pass the project id of the service, as in the Errors table row.")
	case !slices.Contains(d.Config.Projects, project):
		return (&APIError{Status: http.StatusBadRequest, Code: "invalid_scope",
			Message: "The project " + quote(project) + " is not a configured project."}).
			WithHint("Use one of the project ids from GET /api/v1/config.")
	}
	minStatus, limit := 400, errorsrc.DefaultSampleLimit
	if v := q.Get("min_status"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 100 || n > 599 {
			return ErrInvalidRequest("min_status must be an integer HTTP status between 100 and 599.")
		}
		minStatus = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > errorsrc.MaxSampleLimit {
			return ErrInvalidRequest("limit must be an integer between 1 and " + strconv.Itoa(errorsrc.MaxSampleLimit) + ".")
		}
		limit = n
	}
	if d.Samples == nil {
		return (&APIError{Status: http.StatusServiceUnavailable, Code: "samples_unavailable",
			Message: "The Cloud Logging client could not be built, so log samples are unavailable."}).
			WithHint("Check Application Default Credentials and grant roles/logging.viewer; see the server log (logging.samples_client).")
	}
	win := gcpsource.Window{Start: week.Start(), End: week.End()}
	res, err := errorsrc.SampleErrors(r.Context(), d.Samples, errorsrc.SampleParams{
		Project: project, Service: service, Window: win, MinStatus: minStatus, Limit: limit, Now: now})
	if err != nil {
		status, msg, hint := gcpsource.Classify(err, errorsrc.LoggingRoleHint, errorsrc.LoggingAPI)
		e := &APIError{Status: http.StatusBadGateway, Code: "samples_failed", Message: "Reading Cloud Logging failed: " + msg}
		switch status {
		case gcpsource.StatusPermissionDenied:
			e.Status, e.Code = http.StatusForbidden, "permission_denied"
		case gcpsource.StatusAPIDisabled:
			e.Status, e.Code = http.StatusServiceUnavailable, "api_disabled"
		case gcpsource.StatusQuota:
			e.Status, e.Code = http.StatusTooManyRequests, "quota"
		}
		if hint == "" {
			hint = "Check the roles/logging.viewer grant and retry."
		}
		return e.WithHint(hint)
	}
	writeJSON(w, http.StatusOK, samplesResponse{Week: week.String(), Project: project, Service: service, MinStatus: minStatus,
		GeneratedAt: now.Truncate(time.Second), LogsURL: errorsrc.LogsExplorerURL(project, service, win), SampleResult: res})
	return nil
}
