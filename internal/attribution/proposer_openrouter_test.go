package attribution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "placeholder-key-do-not-leak"

func sp(s string) *string { return &s }

func testItems() []UnattributedItem {
	return []UnattributedItem{
		{Kind: "resource", Key: "k1", ProjectID: "proj-a", Service: "Compute Engine", ResourceName: sp("example-orphan-1"), Net: 1234.56, SharePct: 61.7, LineItems: 4242, Reason: "no rule matched"},
		{Kind: "resource", Key: "k2", ProjectID: "proj-a", Service: "Compute Engine", ResourceName: sp("example-orphan-2"), Net: 99.5},
		{Kind: "sku", Key: "k3", ProjectID: "proj-b", Service: "Cloud Storage", SKU: sp("Standard Storage"), Net: 7.25},
	}
}

func completion(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
		"usage":   map[string]any{"prompt_tokens": 111, "completion_tokens": 22},
	})
	return string(b)
}

const goodRules = `{"rules":[
 {"match":{"field":"resource_name","op":"contains","value":"example-orphan"},"action":{"product":"Example-Orphan"},"confidence":"medium","rationale":"Two resources share the token."},
 {"match":{"field":"service","op":"equals","value":"Cloud Storage"},"action":{"shared":true},"confidence":"HIGH","rationale":"Shared storage."}
]}`

func newTestProposer(t *testing.T, h http.HandlerFunc, model string) *openRouterProposer {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p := NewOpenRouterProposer(testKey, model, srv.Client()).(*openRouterProposer)
	p.endpoint = srv.URL
	p.backoff = time.Millisecond
	return p
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestOpenRouter_HappyPath(t *testing.T) {
	logs := captureLogs(t)
	var gotAuth string
	var gotBody []byte
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, completion(goodRules))
	}, "")
	out, err := p.Propose(context.Background(), ProposeInput{Items: testItems(), MaxRules: 5})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+testKey {
		t.Errorf("authorization header not sent")
	}
	if out.Model != DefaultProposerModel || out.InputTokens != 111 || out.OutputTokens != 22 || out.Dropped != 0 {
		t.Errorf("output meta wrong: %+v", out)
	}
	if len(out.Proposals) != 2 {
		t.Fatalf("proposals = %d", len(out.Proposals))
	}
	a, b := out.Proposals[0], out.Proposals[1]
	if a.Rule.Enabled || b.Rule.Enabled || a.Rule.Source != SourceProposed || a.Rule.ID != nil {
		t.Errorf("proposed rules must be disabled/proposed/id-less: %+v", a.Rule)
	}
	if a.Rule.Action.Product != "example-orphan" || a.Covers != 2 || a.Confidence != ConfidenceMedium {
		t.Errorf("first proposal wrong: %+v", a)
	}
	if !b.Rule.Action.Shared || b.Covers != 1 || b.Confidence != ConfidenceHigh || b.Rule.Priority != a.Rule.Priority+1 {
		t.Errorf("second proposal wrong: %+v", b)
	}
	// request shape and privacy
	var req orRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != DefaultProposerModel || len(req.Messages) != 2 || req.Temperature != 0 {
		t.Errorf("request shape wrong: %+v", req.Model)
	}
	for _, bad := range []string{"1234", "99.5", "7.25", "61.7", "4242", testKey} {
		if strings.Contains(string(gotBody), bad) {
			t.Errorf("request body leaks %q", bad)
		}
	}
	userMsg := req.Messages[1].Content
	for _, want := range []string{"example-orphan-1", "Compute Engine", "proj-b", `"rank":1`, "Propose at most 5 rules"} {
		if !strings.Contains(userMsg, want) {
			t.Errorf("request body lacks %q", want)
		}
	}
	// logging: metadata only
	l := logs.String()
	if !strings.Contains(l, "input_tokens=111") {
		t.Errorf("call not logged: %q", l)
	}
	for _, bad := range []string{testKey, "example-orphan", "Compute Engine", "Authorization"} {
		if strings.Contains(l, bad) {
			t.Errorf("logs leak %q", bad)
		}
	}
}

func TestOpenRouter_ModelOverride(t *testing.T) {
	var req orRequest
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = io.WriteString(w, completion(`{"rules":[]}`))
	}, " vendor/model-x ")
	out, err := p.Propose(context.Background(), ProposeInput{Items: testItems()})
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "vendor/model-x" || out.Model != "vendor/model-x" || p.Model() != "vendor/model-x" {
		t.Errorf("model = %q / %q", req.Model, out.Model)
	}
	if out.Proposals == nil || len(out.Proposals) != 0 {
		t.Errorf("empty result must be a non-nil empty slice")
	}
}

func TestOpenRouter_DefaultModelAndDisabled(t *testing.T) {
	p := NewOpenRouterProposer("", "", nil)
	if p.Enabled() || p.Model() != "" {
		t.Errorf("no key must disable: enabled=%v model=%q", p.Enabled(), p.Model())
	}
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&calls, 1) }))
	defer srv.Close()
	p.(*openRouterProposer).endpoint = srv.URL
	if _, err := p.Propose(context.Background(), ProposeInput{Items: testItems()}); !errors.Is(err, ErrProposerNotConfigured) {
		t.Errorf("err = %v, want ErrProposerNotConfigured", err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("disabled proposer made a network call")
	}
	q := NewOpenRouterProposer(testKey, "", nil)
	if !q.Enabled() || q.Model() != "anthropic/claude-sonnet-4-6" {
		t.Errorf("default model wrong: %q", q.Model())
	}
}

func TestOpenRouter_CodeFencedAndProse(t *testing.T) {
	for name, content := range map[string]string{
		"fenced json":   "```json\n" + goodRules + "\n```",
		"fenced plain":  "```\n" + goodRules + "\n```",
		"with prose":    "Here you go:\n```json\n" + goodRules + "\n```\nHope it helps.",
		"bare array":    `[{"match":{"field":"service","op":"equals","value":"Cloud Storage"},"action":{"shared":true}}]`,
		"unfenced text": "Sure! " + goodRules + " Done.",
	} {
		t.Run(name, func(t *testing.T) {
			p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, completion(content))
			}, "")
			out, err := p.Propose(context.Background(), ProposeInput{Items: testItems()})
			if err != nil || len(out.Proposals) == 0 || out.Dropped != 0 {
				t.Fatalf("err=%v out=%+v", err, out)
			}
		})
	}
}

func TestOpenRouter_InvalidRulesDroppedAndBombRejected(t *testing.T) {
	bomb := strings.Repeat("(a+)+", 60) // 300 chars: over the regex bound
	content := `{"rules":[
 {"match":{"field":"nope","op":"contains","value":"x"},"action":{"product":"p"}},
 {"match":{"field":"service","op":"weird","value":"x"},"action":{"product":"p"}},
 {"match":{"field":"service","op":"contains","value":""},"action":{"product":"p"}},
 {"match":{"field":"label","op":"equals","value":"x"},"action":{"product":"p"}},
 {"match":{"field":"service","op":"equals","key":"k","value":"x"},"action":{"product":"p"}},
 {"match":{"field":"service","op":"equals","value":"x"},"action":{}},
 {"match":{"field":"service","op":"equals","value":"x"},"action":{"product":"p","shared":true}},
 {"match":{"field":"service","op":"equals","value":"x"},"action":{"product":"Bad Name!"}},
 {"match":{"field":"service","op":"regex","value":"(unclosed"},"action":{"product":"p"}},
 {"match":{"field":"resource_name","op":"regex","value":"` + bomb + `"},"action":{"product":"p"}},
 "not an object",
 {"match":{"field":"project","op":"equals","value":"proj-b"},"action":{"product":"good-one","environment":"prod"},"confidence":"weird"},
 {"match":{"field":"project","op":"equals","value":"PROJ-B"},"action":{"product":"good-one","environment":"prod"}}
]}`
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, completion(content))
	}, "")
	out, err := p.Propose(context.Background(), ProposeInput{Items: testItems()})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Proposals) != 1 || out.Dropped != 12 {
		t.Fatalf("proposals=%d dropped=%d", len(out.Proposals), out.Dropped)
	}
	if out.Proposals[0].Confidence != ConfidenceLow || out.Proposals[0].Covers != 1 {
		t.Errorf("proposal = %+v", out.Proposals[0])
	}
}

func TestOpenRouter_CapsRuleCount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"rules":[`)
	for i := 0; i < 30; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"match":{"field":"resource_name","op":"prefix","value":"r` + strings.Repeat("x", i+1) + `"},"action":{"product":"p"}}`)
	}
	sb.WriteString(`]}`)
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, completion(sb.String()))
	}, "")
	out, err := p.Propose(context.Background(), ProposeInput{Items: testItems(), MaxRules: 7})
	if err != nil || len(out.Proposals) != 7 || out.Dropped != 23 {
		t.Fatalf("err=%v proposals=%d dropped=%d", err, len(out.Proposals), out.Dropped)
	}
	out, _ = p.Propose(context.Background(), ProposeInput{Items: testItems()})
	if len(out.Proposals) != DefaultMaxRules {
		t.Errorf("default cap = %d", len(out.Proposals))
	}
}

func TestOpenRouter_RetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, completion(goodRules))
	}, "")
	out, err := p.Propose(context.Background(), ProposeInput{Items: testItems()})
	if err != nil || len(out.Proposals) != 2 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("err=%v calls=%d out=%+v", err, calls, out)
	}
}

func TestOpenRouter_ServerErrorFailsAfterOneRetry(t *testing.T) {
	var calls int32
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
	}, "")
	_, err := p.Propose(context.Background(), ProposeInput{Items: testItems()})
	if !errors.Is(err, ErrProposerFailed) || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("err = %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("error leaks key")
	}
}

func TestOpenRouter_ClientErrorNotRetried(t *testing.T) {
	var calls int32
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}, "")
	_, err := p.Propose(context.Background(), ProposeInput{Items: testItems()})
	if !errors.Is(err, ErrProposerFailed) || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestOpenRouter_BadOutputIsProposerFailed(t *testing.T) {
	for name, body := range map[string]string{
		"not json provider body": `<html>oops</html>`,
		"no choices":             `{"choices":[]}`,
		"prose only":             completion("I cannot help with that."),
		"wrong shape":            completion(`{"foo":1}`),
	} {
		t.Run(name, func(t *testing.T) {
			p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }, "")
			if _, err := p.Propose(context.Background(), ProposeInput{Items: testItems()}); !errors.Is(err, ErrProposerFailed) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestOpenRouter_ContextCancelled(t *testing.T) {
	p := newTestProposer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}, "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Propose(ctx, ProposeInput{Items: testItems()}); !errors.Is(err, ErrProposerFailed) {
		t.Errorf("err = %v", err)
	}
}

func TestPrompt_CapsResourcesAndIsDeterministic(t *testing.T) {
	var items []UnattributedItem
	for i := 0; i < 400; i++ {
		items = append(items, UnattributedItem{Kind: "resource", ProjectID: "p", Service: "s", ResourceName: sp("res-" + strings.Repeat("a", i%7) + string(rune('a'+i%26))), Net: float64(i)})
	}
	rule := Rule{Enabled: true, Match: Match{Field: FieldService, Op: OpEquals, Value: "VM Manager"}, Action: Action{Shared: true}}
	in := ProposeInput{Items: items, Existing: []Rule{rule}}
	s1, u1 := buildProposerPrompt(in, 20)
	s2, u2 := buildProposerPrompt(in, 20)
	if s1 != s2 || u1 != u2 {
		t.Fatal("prompt not deterministic")
	}
	if n := strings.Count(u1, `"rank":`); n != maxProposeResources {
		t.Errorf("resources in prompt = %d", n)
	}
	if !strings.Contains(u1, `"rank":1,`) || !strings.Contains(u1, "VM Manager") || strings.Contains(u1, "399") {
		t.Errorf("ranking/existing rules wrong")
	}
	if !strings.Contains(s1, "RE2") || !strings.Contains(s1, "resource_name") {
		t.Errorf("system prompt lacks schema")
	}
}
