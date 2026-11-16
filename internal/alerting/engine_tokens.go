package alerting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// engTokenTTL is the lifetime of a preview token.
const engTokenTTL = 10 * time.Minute

// engPending is everything Apply needs, kept in memory only (a restart
// invalidates every token). Sensitive channel labels live here and nowhere else.
type engPending struct {
	token, hash string
	expires     time.Time

	kind, action, target, project string
	current, desired              json.RawMessage
	currentHash                   string
	requiresConfirm               bool
	warnings                      []string

	policyBody  json.RawMessage // policy create/update body
	enabled     bool            // policy enable/disable
	uptime      UptimeDraft
	budget      BudgetDraft
	budgetChans []string
	snooze      SnoozeDraft
	channel     ChannelDraft
	extraIgnore map[string]bool // diff ignore keys
}

type engTokens struct {
	mu sync.Mutex
	m  map[string]*engPending
}

func newEngTokens() *engTokens { return &engTokens{m: map[string]*engPending{}} }

func (t *engTokens) put(p *engPending, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if !now.Before(v.expires) {
			delete(t.m, k)
		}
	}
	t.m[p.token] = p
}

// claim returns the pending entry and removes it (single use). Unknown or
// expired → ErrPreviewStale. A delete without confirm keeps the entry.
func (t *engTokens) claim(token string, now time.Time, confirm bool) (*engPending, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[token]
	if !ok {
		return nil, fmt.Errorf("%w: unknown, expired or already used preview token", ErrPreviewStale)
	}
	if !now.Before(p.expires) {
		delete(t.m, token)
		return nil, fmt.Errorf("%w: preview token expired", ErrPreviewStale)
	}
	if p.requiresConfirm && !confirm {
		return nil, fmt.Errorf("%w: resend with confirm_delete after reviewing the delete preview", ErrConfirmRequired)
	}
	delete(t.m, token)
	return p, nil
}

func engHashBytes(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// engCurrentHash hashes the canonical JSON of the live resource ("null" when absent).
func engCurrentHash(raw json.RawMessage) string {
	v, err := engDecode(raw)
	if err != nil || v == nil {
		return engHashBytes([]byte("null"))
	}
	return engHashBytes(engCanonical(v))
}

// engSecretDigest folds sensitive channel labels into the token so two
// previews differing only in a secret never share a token. Not reversible
// and never persisted.
func engSecretDigest(m map[string]string) []byte {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s=%d:%s;", len(k), k, len(m[k]), m[k])
	}
	return h.Sum(nil)
}

// engToken = "pv_" + hex SHA-256 over (kind, action, target, canonical desired, live hash).
func engToken(p *engPending) (token, hash string) {
	d, _ := engDecode(p.desired)
	hash = engHashBytes([]byte(p.kind), []byte(p.action), []byte(p.target), engCanonical(d), []byte(p.currentHash), engSecretDigest(p.channel.SensitiveLabels))
	return "pv_" + hash, hash
}
