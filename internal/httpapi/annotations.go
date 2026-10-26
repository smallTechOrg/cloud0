package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/smallTechOrg/cloud0/internal/store"
)

func (d Deps) handleGetAnnotation(w http.ResponseWriter, r *http.Request) *APIError {
	week, aerr := parseWeek(r.PathValue("week"))
	if aerr != nil {
		return aerr
	}
	a, err := store.GetAnnotation(r.Context(), d.Store.DB, week)
	if err != nil {
		return storeErr(err, "No annotation for "+week.String()+".")
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (d Deps) handlePutAnnotation(w http.ResponseWriter, r *http.Request) *APIError {
	week, aerr := parseWeek(r.PathValue("week"))
	if aerr != nil {
		return aerr
	}
	var body struct {
		Body *string `json:"body"`
	}
	if aerr := decodeBody(w, r, &body); aerr != nil {
		return aerr
	}
	if body.Body == nil {
		return ErrInvalidRequest("The body field is required.").WithHint(`Send {"body": "text"}; an empty string deletes the note.`)
	}
	tooLong := func() *APIError {
		return (&APIError{Status: http.StatusBadRequest, Code: "annotation_too_long", Message: "The note is longer than 10,000 characters."}).
			WithHint("Shorten the note to 10,000 characters or fewer.")
	}
	if utf8.RuneCountInString(*body.Body) > store.MaxAnnotationLen {
		return tooLong()
	}
	ctx := r.Context()
	if strings.TrimSpace(*body.Body) == "" {
		if err := store.DeleteAnnotation(ctx, d.Store.DB, week); err != nil {
			return ErrInternal(err)
		}
		writeJSON(w, http.StatusOK, store.Annotation{Week: week.String(), Body: ""})
		return nil
	}
	a, err := store.UpsertAnnotation(ctx, d.Store.DB, week, *body.Body, d.now())
	if errors.Is(err, store.ErrAnnotationTooLong) {
		return tooLong()
	}
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}
