package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/labeler"
	"github.com/smallTechOrg/cloud0/internal/store"
)

const (
	labelsChangeKind = "labels"
	maxLabelItems    = 100
	labelsHint       = "Set CLOUD0_LABELS_CREDENTIALS to the cloud0-labels service-account key path and restart — see README → Labels setup."
)

func (d Deps) registerLabels(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/attribution/resources", d.handleAttrResources)
	r.handle(http.MethodGet, apiPrefix+"/attribution/export", d.handleAttrExport)
	r.handle(http.MethodPost, apiPrefix+"/labels/preview", d.handleLabelsPreview)
	r.handle(http.MethodPost, apiPrefix+"/labels/apply", d.handleLabelsApply)
}

func errLabelsNotConfigured() *APIError {
	return (&APIError{Status: http.StatusServiceUnavailable, Code: "labels_not_configured",
		Message: "GCP label writes are not configured."}).WithHint(labelsHint)
}

// ---- GET /attribution/resources -----------------------------------------------

func (d Deps) handleAttrResources(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	ac, aerr := d.attrLoad(r.Context(), ad, q.Get("week"), q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	resp := attribution.ResourcesResponse{
		Week: ac.week.String(), Scope: ac.scope, Snapshot: ac.snapshot,
		LabelsCaptured: ac.labels && ac.snap != nil,
		Groups:         []attribution.ResourceGroup{}, Products: []attribution.ResourceProduct{}, Environments: []string{},
		LabelWriter: attribution.LabelWriterInfo{Enabled: d.Labeler != nil},
	}
	if d.Labeler == nil {
		resp.LabelWriter.Hint = labelsHint
	}
	if ac.snap != nil {
		suggest := func(row attribution.Row) *attribution.Suggested { return attrSuggest(ac.eng, ac.rules, row) }
		groups, products, envs, total := attribution.BuildResources(ac.cost, ac.eng, suggest)
		resp.TotalNet, resp.Count = total, len(groups)
		const cap = 1500
		if len(groups) > cap {
			groups, resp.Truncated = groups[:cap], true
		}
		resp.Groups, resp.Products, resp.Environments = groups, products, envs
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// ---- preview / apply ------------------------------------------------------------

type labelItemIn struct {
	Target labeler.Target    `json:"target"`
	Labels map[string]string `json:"labels"`
}

type labelRequest struct {
	Items       []labelItemIn `json:"items"`
	PreviewHash string        `json:"preview_hash"`
}

type labelErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type labelItemOut struct {
	Target    labeler.Target    `json:"target"`
	Current   map[string]string `json:"current"`
	Add       map[string]string `json:"add"`
	Diff      []labeler.Diff    `json:"diff"`
	NoOp      bool              `json:"noop"`
	Command   *string           `json:"command"`
	Terraform string            `json:"terraform"`
	Risk      string            `json:"risk"`
	After     map[string]string `json:"after,omitempty"`
	Applied   bool              `json:"applied"`
	Error     *labelErr         `json:"error"`
}

type labelResponse struct {
	PreviewHash string         `json:"preview_hash"`
	Items       []labelItemOut `json:"items"`
	Changes     int            `json:"changes"`
	Applied     int            `json:"applied"`
	Failed      int            `json:"failed"`
	Note        string         `json:"note"`
}

const labelNote = "GCP labels reach the billing export only for usage after they are written, so past weeks keep their cloud0 rule attribution. Labels are metadata only; the next Refresh after a day of new usage shows them."

func (d Deps) labeler() (labeler.Client, *APIError) {
	if d.Labeler == nil {
		return nil, errLabelsNotConfigured()
	}
	return d.Labeler, nil
}

func validateLabelRequest(req *labelRequest) *APIError {
	if len(req.Items) == 0 {
		return errInvalidParams("items must not be empty.")
	}
	if len(req.Items) > maxLabelItems {
		return errInvalidParams("at most 100 items per request.")
	}
	for i, it := range req.Items {
		if err := labeler.ValidTarget(it.Target); err != nil {
			return errInvalidParams("items[" + strconv.Itoa(i) + "]: " + err.Error() + ".")
		}
		if len(it.Labels) == 0 || len(it.Labels) > 8 {
			return errInvalidParams("items[" + strconv.Itoa(i) + "]: labels must have 1–8 entries.")
		}
		for k, v := range it.Labels {
			if !labeler.ValidLabel(k, v) || v == "" {
				return errInvalidParams("items[" + strconv.Itoa(i) + "]: label " + quoteShort(k) + " has an invalid key or value (lowercase letters, digits, _ and -; max 63).")
			}
		}
	}
	return nil
}

// plan reads live labels for every item (bounded parallelism) and builds the
// preview. It never writes.
func (d Deps) labelPlan(ctx context.Context, c labeler.Client, req labelRequest) labelResponse {
	out := make([]labelItemOut, len(req.Items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i := range req.Items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			it := req.Items[i]
			o := labelItemOut{Target: it.Target, Add: it.Labels, Current: map[string]string{}, Diff: []labeler.Diff{}}
			cmd, risk := attribution.LabelCommand(it.Target.Type, it.Target.Project, it.Target.Name, it.Target.Location, it.Labels)
			o.Command, o.Risk = cmd, risk
			cur, err := c.Get(ctx, it.Target)
			if err != nil {
				o.Error = toLabelErr(err)
			} else {
				o.Current = cur
				o.Diff = labeler.PlanDiff(cur, it.Labels)
				o.Terraform = labeler.Terraform(it.Target, labeler.Merged(cur, it.Labels))
				o.NoOp = true
				for _, df := range o.Diff {
					if df.Change != "same" {
						o.NoOp = false
					}
				}
			}
			out[i] = o
		}(i)
	}
	wg.Wait()
	resp := labelResponse{Items: out, Note: labelNote}
	for _, o := range out {
		if o.Error == nil && !o.NoOp {
			resp.Changes++
		}
	}
	resp.PreviewHash = labelHash(out)
	return resp
}

func toLabelErr(err error) *labelErr {
	var le *labeler.Error
	if errors.As(err, &le) {
		return &labelErr{Code: le.Code, Message: le.Message, Hint: le.Hint}
	}
	return &labelErr{Code: labeler.CodeFailed, Message: err.Error()}
}

// labelHash fingerprints exactly what a click would change: targets, live
// labels and labels to add. Apply recomputes it; a mismatch means stale.
func labelHash(items []labelItemOut) string {
	h := sha256.New()
	for _, o := range items {
		h.Write([]byte(o.Target.String() + "\n"))
		h.Write([]byte(sortedKV(o.Current) + "\n" + sortedKV(o.Add) + "\n"))
		if o.Error != nil {
			h.Write([]byte("err:" + o.Error.Code + "\n"))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedKV(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + m[k] + ";")
	}
	return b.String()
}

func (d Deps) handleLabelsPreview(w http.ResponseWriter, r *http.Request) *APIError {
	c, aerr := d.labeler()
	if aerr != nil {
		return aerr
	}
	var req labelRequest
	if aerr := decodeAlertsBody(w, r, &req); aerr != nil {
		return aerr
	}
	if aerr := validateLabelRequest(&req); aerr != nil {
		return aerr
	}
	writeJSON(w, http.StatusOK, d.labelPlan(r.Context(), c, req))
	return nil
}

func (d Deps) handleLabelsApply(w http.ResponseWriter, r *http.Request) *APIError {
	c, aerr := d.labeler()
	if aerr != nil {
		return aerr
	}
	var req labelRequest
	if aerr := decodeAlertsBody(w, r, &req); aerr != nil {
		return aerr
	}
	if aerr := validateLabelRequest(&req); aerr != nil {
		return aerr
	}
	if req.PreviewHash == "" {
		return errInvalidParams("preview_hash is required; call /labels/preview first.")
	}
	plan := d.labelPlan(r.Context(), c, req)
	if plan.PreviewHash != req.PreviewHash {
		return (&APIError{Status: http.StatusConflict, Code: "preview_stale",
			Message: "The labels on the resources changed since the preview."}).WithHint("Preview again, review the new diff, then apply.")
	}
	for i := range plan.Items {
		o := &plan.Items[i]
		if o.Error != nil || o.NoOp {
			if o.Error != nil {
				plan.Failed++
			}
			continue
		}
		after, err := c.Merge(r.Context(), o.Target, o.Add)
		if err != nil {
			o.Error = toLabelErr(err)
			plan.Failed++
			d.Logger.Warn("labels.apply_failed", "target", o.Target.String(), "error", o.Error.Code)
			continue
		}
		o.After, o.Applied = after, true
		plan.Applied++
		before, _ := json.Marshal(o.Current)
		aft, _ := json.Marshal(after)
		if _, herr := store.InsertChange(r.Context(), d.Store.DB, store.NewChange{
			At: d.now(), Kind: labelsChangeKind, Action: "update", Target: o.Target.String(),
			Before: before, After: aft, PreviewHash: plan.PreviewHash,
		}); herr != nil {
			d.Logger.Error("labels.history_failed", "target", o.Target.String(), "error", herr.Error())
		}
		d.Logger.Info("labels.applied", "target", o.Target.String())
	}
	writeJSON(w, http.StatusOK, plan)
	return nil
}
