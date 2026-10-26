package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

// MaxAnnotationLen is the body limit in characters (spec/data.md → annotations).
const MaxAnnotationLen = 10000

// ErrAnnotationTooLong is returned for a body over MaxAnnotationLen characters.
var ErrAnnotationTooLong = errors.New("store: annotation too long")

// Annotation is the GET/PUT /api/v1/annotations/{week} body. CreatedAt and
// UpdatedAt are nil only in the "deleted" response of an empty PUT.
type Annotation struct {
	Week      string     `json:"week"`
	Body      string     `json:"body"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// GetAnnotation returns the note for a week; ErrNotFound when there is none.
func GetAnnotation(ctx context.Context, q Querier, week isoweek.Week) (Annotation, error) {
	var body, created, updated string
	err := q.QueryRowContext(ctx, `SELECT body, created_at, updated_at FROM annotations WHERE iso_year = ? AND iso_week = ?`, week.Year, week.Week).Scan(&body, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Annotation{}, fmt.Errorf("%w: annotation for %s", ErrNotFound, week)
	}
	if err != nil {
		return Annotation{}, fmt.Errorf("store: get annotation %s: %w", week, err)
	}
	c, err := ParseTime(created)
	if err != nil {
		return Annotation{}, fmt.Errorf("store: annotation %s created_at %q: %w", week, created, err)
	}
	u, err := ParseTime(updated)
	if err != nil {
		return Annotation{}, fmt.Errorf("store: annotation %s updated_at %q: %w", week, updated, err)
	}
	return Annotation{Week: week.String(), Body: body, CreatedAt: &c, UpdatedAt: &u}, nil
}

// UpsertAnnotation saves the note for a week (body stored verbatim). A body
// that is empty after trimming deletes the note and returns the deleted shape
// (nil timestamps). created_at is kept on update; updated_at is set to now.
func UpsertAnnotation(ctx context.Context, q Querier, week isoweek.Week, body string, now time.Time) (Annotation, error) {
	if !week.Valid() {
		return Annotation{}, fmt.Errorf("%w: week %v does not exist", ErrInvalidInput, week)
	}
	if now.IsZero() {
		return Annotation{}, fmt.Errorf("%w: now required", ErrInvalidInput)
	}
	if n := utf8.RuneCountInString(body); n > MaxAnnotationLen {
		return Annotation{}, fmt.Errorf("%w: %d characters, max %d", ErrAnnotationTooLong, n, MaxAnnotationLen)
	}
	if strings.TrimSpace(body) == "" {
		if err := DeleteAnnotation(ctx, q, week); err != nil {
			return Annotation{}, err
		}
		return Annotation{Week: week.String()}, nil
	}
	ts := FormatTime(now.UTC().Truncate(time.Second))
	// ON CONFLICT … DO UPDATE is shared by SQLite (≥ 3.24) and Postgres, so one
	// statement stays atomic on both engines.
	_, err := q.ExecContext(ctx, `INSERT INTO annotations (iso_year, iso_week, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (iso_year, iso_week) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		week.Year, week.Week, body, ts, ts)
	if err != nil {
		return Annotation{}, fmt.Errorf("store: upsert annotation %s: %w", week, err)
	}
	return GetAnnotation(ctx, q, week)
}

// DeleteAnnotation removes the note for a week; deleting a missing note is not an error.
func DeleteAnnotation(ctx context.Context, q Querier, week isoweek.Week) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM annotations WHERE iso_year = ? AND iso_week = ?`, week.Year, week.Week); err != nil {
		return fmt.Errorf("store: delete annotation %s: %w", week, err)
	}
	return nil
}

// AnnotationsInRange returns the note bodies keyed by week for first..last
// inclusive (the cost table's `annotation` column).
func AnnotationsInRange(ctx context.Context, q Querier, first, last isoweek.Week) (map[isoweek.Week]string, error) {
	if !first.Valid() || !last.Valid() || isoweek.Compare(first, last) > 0 {
		return nil, fmt.Errorf("%w: week range %v..%v", ErrInvalidInput, first, last)
	}
	rows, err := q.QueryContext(ctx, `SELECT iso_year, iso_week, body FROM annotations WHERE (iso_year * 100 + iso_week) BETWEEN ? AND ?`, weekOrdinal(first), weekOrdinal(last))
	if err != nil {
		return nil, fmt.Errorf("store: annotations in range: %w", err)
	}
	defer rows.Close()
	out := make(map[isoweek.Week]string)
	for rows.Next() {
		var w isoweek.Week
		var body string
		if err := rows.Scan(&w.Year, &w.Week, &body); err != nil {
			return nil, fmt.Errorf("store: annotations in range: %w", err)
		}
		out[w] = body
	}
	return out, rows.Err()
}
