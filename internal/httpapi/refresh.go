package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/refresh"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// refreshRun is the run object of POST /refresh: the run without
// snapshot_ids, and sources without sql / params.
type refreshRun struct {
	ID         int64             `json:"id"`
	Trigger    store.RunTrigger  `json:"trigger"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	DurationMs *int64            `json:"duration_ms"`
	Status     store.RunStatus   `json:"status"`
	Error      *string           `json:"error"`
	Sources    []store.RunSource `json:"sources"`
}

type refreshResponse struct {
	Run       refreshRun              `json:"run"`
	Snapshots []store.SnapshotSummary `json:"snapshots"`
}

func (d Deps) handleRefresh(w http.ResponseWriter, r *http.Request) *APIError {
	var body struct {
		Mode *string `json:"mode"`
		Week *string `json:"week"`
	}
	if aerr := decodeBody(w, r, &body); aerr != nil {
		return aerr
	}
	if body.Mode == nil || (*body.Mode != string(refresh.ModeWeek) && *body.Mode != string(refresh.ModeAll)) {
		return ErrInvalidRequest(`The mode field must be "week" or "all".`).
			WithHint(`Send {"mode":"week","week":"2026-W43"} or {"mode":"all"}.`)
	}
	mode := refresh.Mode(*body.Mode)
	var week isoweek.Week
	if mode == refresh.ModeWeek {
		if body.Week == nil {
			return ErrInvalidRequest(`mode "week" requires the week field.`).
				WithHint(`Send {"mode":"week","week":"2026-W43"}.`)
		}
		var aerr *APIError
		if week, aerr = parseWeek(*body.Week); aerr != nil {
			return aerr
		}
	}
	if d.Refresh == nil {
		return ErrInternal(errors.New("refresh runner not configured"))
	}

	res, err := d.Refresh.Run(r.Context(), mode, week)
	switch {
	case errors.Is(err, refresh.ErrBusy):
		return (&APIError{Status: http.StatusConflict, Code: "refresh_in_progress", Message: "Another refresh is already running."}).
			WithHint("Wait for the current refresh to finish, then retry.")
	case errors.Is(err, refresh.ErrInvalidMode):
		return ErrInvalidRequest(`The mode field must be "week" or "all".`)
	case errors.Is(err, refresh.ErrInvalidWeek):
		_, aerr := parseWeek("invalid")
		return aerr
	case errors.Is(err, refresh.ErrWeekOutOfRange):
		return errWeekOutOfRange("The requested week is outside " + d.Config.FirstWeek.String() + ".." + d.Refresh.Current().String() + ".")
	case err != nil:
		return ErrInternal(err)
	}

	run := res.Run.WithoutSQL()
	writeJSON(w, http.StatusOK, refreshResponse{
		Run: refreshRun{ID: run.ID, Trigger: run.Trigger, From: run.From, To: run.To, StartedAt: run.StartedAt,
			FinishedAt: run.FinishedAt, DurationMs: run.DurationMs, Status: run.Status, Error: run.Error, Sources: run.Sources},
		Snapshots: res.Snapshots,
	})
	return nil
}
