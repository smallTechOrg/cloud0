package costtable

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

// Layout decides which service columns the Cost table (and the Month and
// breakdown views that share its columns) shows. It comes from configuration,
// never from code, so any deployment gets a table that fits its own billing:
//
//   - Dynamic (the default, CLOUD0_COST_COLUMNS unset): one column per billing
//     service.description that has a non-zero net or gross anywhere in the
//     range, ordered by the range's net total (largest first, then label).
//   - Pinned (CLOUD0_COST_COLUMNS set): the listed columns always appear, in
//     the listed order, each aggregating one or more service descriptions;
//     any other service with a non-zero net in the range is appended as an
//     extra column, sorted by label.
//
// Vendor stub columns (CLOUD0_VENDOR_COLUMNS) are appended after % change;
// none by default.
type Layout struct {
	pinned  []Column
	vendors []Column
	keys    map[string]string // normalised service.description → pinned key
}

// DynamicLayout is the zero-configuration layout.
func DynamicLayout() Layout { return Layout{} }

// Pinned reports whether the layout lists fixed service columns.
func (l Layout) Pinned() bool { return len(l.pinned) > 0 }

// KeyFor maps a service.description onto its column key.
func (l Layout) KeyFor(service string) string {
	if key, ok := l.keys[normalizeService(service)]; ok {
		return key
	}
	return ExtraKeyPrefix + strings.TrimSpace(service)
}

// Columns builds the ordered header: week, the pinned service columns, one
// extra column per present unmapped service, then total, % change and the
// vendor stubs. totals holds the range total per unmapped service
// description (keys are trimmed; duplicates after trimming are merged).
func (l Layout) Columns(totals map[string]ServiceTotal) []Column {
	merged := make(map[string]ServiceTotal, len(totals))
	for s, t := range totals {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := l.keys[normalizeService(s)]; ok {
			continue // a pinned service is never an extra
		}
		m := merged[s]
		m.Net += t.Net
		m.Gross += t.Gross
		merged[s] = m
	}
	extras := make([]string, 0, len(merged))
	for s, t := range merged {
		present := t.Net != 0
		if !l.Pinned() {
			present = present || t.Gross != 0 // fully-credited services still show in the dynamic layout
		}
		if present {
			extras = append(extras, s)
		}
	}
	if l.Pinned() {
		sort.Strings(extras)
	} else {
		sort.Slice(extras, func(i, j int) bool {
			a, b := merged[extras[i]].Net, merged[extras[j]].Net
			if math.Abs(a-b) > 1e-9 {
				return a > b
			}
			return extras[i] < extras[j]
		})
	}

	out := make([]Column, 0, 1+len(l.pinned)+len(extras)+len(totalColumns)+len(l.vendors))
	out = append(out, weekColumn)
	for _, c := range l.pinned {
		c.Services = append([]string(nil), c.Services...)
		out = append(out, c)
	}
	for _, s := range extras {
		out = append(out, Column{Key: ExtraKeyPrefix + s, Label: s, Kind: KindService, Services: []string{s}, Extra: l.Pinned()})
	}
	out = append(out, totalColumns...)
	return append(out, l.vendors...)
}

var (
	keyPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
	slugReplacer   = regexp.MustCompile(`[^a-z0-9]+`)
	reservedKeys   = map[string]bool{KeyWeek: true, KeyTotal: true, KeyPctChange: true}
	maxLayoutItems = 100
)

// slug turns a label into a column key: "Compute Engine" → "compute_engine".
func slug(label string) string {
	return strings.Trim(slugReplacer.ReplaceAllString(strings.ToLower(label), "_"), "_")
}

// layoutEntry is one parsed `[key:]Label[=Service|Service…]` item.
type layoutEntry struct {
	key, label string
	services   []string
}

// parseEntries splits a `;`-separated layout list. Each entry is
// `[key:]Label[=Service|Service…]`; the key defaults to the label's slug and
// the services to the label itself.
func parseEntries(name, spec string) ([]layoutEntry, error) {
	var out []layoutEntry
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var e layoutEntry
		head, svcs, hasSvcs := strings.Cut(raw, "=")
		if k, label, ok := strings.Cut(head, ":"); ok {
			e.key, e.label = strings.TrimSpace(k), strings.TrimSpace(label)
		} else {
			e.label = strings.TrimSpace(head)
			e.key = slug(e.label)
		}
		if e.label == "" {
			return nil, fmt.Errorf("%s: entry %q has no label", name, raw)
		}
		if !keyPattern.MatchString(e.key) || reservedKeys[e.key] {
			return nil, fmt.Errorf("%s: entry %q has key %q; use lower-case letters, digits and _ (not week, total or pct_change)", name, raw, e.key)
		}
		if seen[e.key] {
			return nil, fmt.Errorf("%s: key %q is listed twice", name, e.key)
		}
		seen[e.key] = true
		if hasSvcs {
			for _, s := range strings.Split(svcs, "|") {
				if s = strings.TrimSpace(s); s != "" {
					e.services = append(e.services, s)
				}
			}
			if len(e.services) == 0 {
				return nil, fmt.Errorf("%s: entry %q lists no service after '='", name, raw)
			}
		} else {
			e.services = []string{e.label}
		}
		out = append(out, e)
		if len(out) > maxLayoutItems {
			return nil, fmt.Errorf("%s: more than %d entries", name, maxLayoutItems)
		}
	}
	return out, nil
}

// ParseLayout builds a Layout from CLOUD0_COST_COLUMNS and
// CLOUD0_VENDOR_COLUMNS. Both empty → DynamicLayout().
//
//	CLOUD0_COST_COLUMNS="Compute Engine; cloud_sql:CloudSQL=Cloud SQL; Gemini API=Gemini API|Generative Language API"
//	CLOUD0_VENDOR_COLUMNS="groq:AI Cost (Groq); claude:claude (usage)"
func ParseLayout(columns, vendors string) (Layout, error) {
	cols, err := parseEntries("CLOUD0_COST_COLUMNS", columns)
	if err != nil {
		return Layout{}, err
	}
	vens, err := parseEntries("CLOUD0_VENDOR_COLUMNS", vendors)
	if err != nil {
		return Layout{}, err
	}
	l := Layout{keys: map[string]string{}}
	for _, e := range cols {
		for _, s := range e.services {
			n := normalizeService(s)
			if other, dup := l.keys[n]; dup {
				return Layout{}, fmt.Errorf("CLOUD0_COST_COLUMNS: service %q is mapped to both %q and %q", s, other, e.key)
			}
			l.keys[n] = e.key
		}
		l.pinned = append(l.pinned, Column{Key: e.key, Label: e.label, Kind: KindService, Services: e.services})
	}
	for _, e := range vens {
		l.vendors = append(l.vendors, Column{Key: StubKeyPrefix + e.key, Label: e.label, Kind: KindStub, Phase: StubPhase})
	}
	return l, nil
}

// MustParseLayout is ParseLayout for literals in tests and examples.
func MustParseLayout(columns, vendors string) Layout {
	l, err := ParseLayout(columns, vendors)
	if err != nil {
		panic(err)
	}
	return l
}

var active atomic.Pointer[Layout]

// SetLayout installs the process-wide layout (cmd/cloud0 does this once at
// start-up from the config; tests install the one they assert on).
func SetLayout(l Layout) { active.Store(&l) }

// ActiveLayout returns the installed layout, DynamicLayout() when none is.
func ActiveLayout() Layout {
	if l := active.Load(); l != nil {
		return *l
	}
	return DynamicLayout()
}

// SheetColumns / SheetVendors reproduce the original "Tech Weekly Review"
// sheet that cloud0 was first built against. They are an example and the
// fixture the tests assert on, not a default: a deployment opts in by
// copying them into CLOUD0_COST_COLUMNS / CLOUD0_VENDOR_COLUMNS.
const (
	SheetColumns = "Compute Engine; Networking; cloud_sql:CloudSQL=Cloud SQL; Gemini API=Gemini API|Generative Language API; " +
		"Cloud Run; GCS=Cloud Storage; VM manager=VM Manager; Cloud Build; Cloud Logging; Secret Manager; Artifact Registry"
	SheetVendors = "groq:AI Cost (Groq); claude:claude (usage)"
)
