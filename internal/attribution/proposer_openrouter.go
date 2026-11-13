package attribution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// OpenRouterEndpoint is the chat-completions URL.
	OpenRouterEndpoint = "https://openrouter.ai/api/v1/chat/completions"
	// DefaultProposerModel is used when no model is configured.
	DefaultProposerModel = "anthropic/claude-sonnet-4-6"
	// DefaultMaxRules and MaxMaxRules bound ProposeInput.MaxRules.
	DefaultMaxRules = 20
	MaxMaxRules     = 50

	proposerTimeout   = 60 * time.Second
	maxResponseBytes  = 1 << 20
	proposedBasePrio  = 330
	maxNotedRationale = 300
)

type openRouterProposer struct {
	apiKey   string
	model    string
	httpc    *http.Client
	endpoint string
	backoff  time.Duration
}

// NewOpenRouterProposer returns the OpenRouter-backed Proposer. It is enabled
// only when apiKey is non-empty; model defaults to DefaultProposerModel and a
// nil httpc to a client with a 60 s timeout.
func NewOpenRouterProposer(apiKey, model string, httpc *http.Client) Proposer {
	apiKey, model = strings.TrimSpace(apiKey), strings.TrimSpace(model)
	if model == "" {
		model = DefaultProposerModel
	}
	if httpc == nil {
		httpc = &http.Client{Timeout: proposerTimeout}
	}
	return &openRouterProposer{apiKey: apiKey, model: model, httpc: httpc, endpoint: OpenRouterEndpoint, backoff: 2 * time.Second}
}

func (p *openRouterProposer) Enabled() bool { return p.apiKey != "" }

func (p *openRouterProposer) Model() string {
	if !p.Enabled() {
		return ""
	}
	return p.model
}

type orMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type orRequest struct {
	Model       string      `json:"model"`
	Messages    []orMessage `json:"messages"`
	Temperature float64     `json:"temperature"`
	MaxTokens   int         `json:"max_tokens"`
}

type orResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func failf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrProposerFailed, fmt.Sprintf(format, a...))
}

// Propose makes one chat completion (plus at most one retry on 429/5xx),
// parses the model's JSON defensively and returns validated, disabled
// proposals. Prompts and the API key are never logged.
func (p *openRouterProposer) Propose(ctx context.Context, in ProposeInput) (ProposeOutput, error) {
	if !p.Enabled() {
		return ProposeOutput{}, ErrProposerNotConfigured
	}
	maxRules := in.MaxRules
	if maxRules <= 0 {
		maxRules = DefaultMaxRules
	}
	if maxRules > MaxMaxRules {
		maxRules = MaxMaxRules
	}
	system, user := buildProposerPrompt(in, maxRules)
	body, err := json.Marshal(orRequest{
		Model:       p.model,
		Messages:    []orMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature: 0,
		MaxTokens:   4000,
	})
	if err != nil {
		return ProposeOutput{}, failf("encode request: %v", err)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, proposerTimeout)
	defer cancel()

	resp, err := p.call(ctx, body)
	if err != nil {
		return ProposeOutput{}, err
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		return ProposeOutput{}, failf("empty completion")
	}
	props, dropped, err := parseProposals(resp.Choices[0].Message.Content, in, maxRules)
	if err != nil {
		return ProposeOutput{}, err
	}
	out := ProposeOutput{
		Model:        p.model,
		LatencyMS:    time.Since(start).Milliseconds(),
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		Proposals:    props,
		Dropped:      dropped,
	}
	slog.Info("attribution proposer call", "model", out.Model, "latency_ms", out.LatencyMS,
		"input_tokens", out.InputTokens, "output_tokens", out.OutputTokens,
		"proposals", len(props), "dropped", dropped)
	return out, nil
}

func (p *openRouterProposer) call(ctx context.Context, body []byte) (orResponse, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return orResponse{}, failf("%v", ctx.Err())
			case <-time.After(p.backoff):
			}
		}
		resp, retry, err := p.once(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return orResponse{}, lastErr
}

// once performs a single HTTP exchange; retry reports a 429/5xx/transport error.
func (p *openRouterProposer) once(ctx context.Context, body []byte) (orResponse, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return orResponse{}, false, failf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("X-Title", "cloud0")
	res, err := p.httpc.Do(req)
	if err != nil {
		// Never include the request (headers) in the message.
		var msg string
		if ctx.Err() != nil {
			msg = ctx.Err().Error()
		} else {
			msg = "transport error"
		}
		return orResponse{}, ctx.Err() == nil, failf("request failed: %s", msg)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return orResponse{}, true, failf("read response: %v", err)
	}
	var parsed orResponse
	_ = json.Unmarshal(raw, &parsed)
	if res.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("provider returned HTTP %d", res.StatusCode)
		if parsed.Error != nil && parsed.Error.Message != "" {
			m := parsed.Error.Message
			if len(m) > 200 {
				m = m[:200]
			}
			msg += ": " + strings.TrimSpace(m)
		}
		retry := res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
		return orResponse{}, retry, failf("%s", msg)
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return orResponse{}, false, failf("provider response is not JSON")
	}
	if parsed.Error != nil && len(parsed.Choices) == 0 {
		return orResponse{}, false, failf("provider error: %s", strings.TrimSpace(parsed.Error.Message))
	}
	return parsed, false, nil
}

type proposedRuleJSON struct {
	Match      Match  `json:"match"`
	Action     Action `json:"action"`
	Confidence string `json:"confidence"`
	Rationale  string `json:"rationale"`
}

// extractJSON strips code fences and surrounding prose, returning the JSON
// text between the first opening and last matching closing bracket.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 && !strings.ContainsAny(rest[:nl], "{[") {
			rest = rest[nl+1:]
		}
		if j := strings.LastIndex(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = strings.TrimSpace(rest)
	}
	oi, ai := strings.IndexByte(s, '{'), strings.IndexByte(s, '[')
	closer := byte('}')
	start := oi
	if oi < 0 || (ai >= 0 && ai < oi) {
		closer, start = ']', ai
	}
	if start < 0 {
		return s
	}
	end := strings.LastIndexByte(s, closer)
	if end < start {
		return s[start:]
	}
	return s[start : end+1]
}

func parseProposals(content string, in ProposeInput, maxRules int) ([]Proposal, int, error) {
	text := extractJSON(content)
	var list []json.RawMessage
	var obj struct {
		Rules *[]json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal([]byte(text), &obj); err == nil && obj.Rules != nil {
		list = *obj.Rules
	} else if err := json.Unmarshal([]byte(text), &list); err != nil {
		return nil, 0, failf("model output is not the expected JSON")
	}

	props := []Proposal{}
	dropped := 0
	seen := map[string]bool{}
	for _, raw := range list {
		var pr proposedRuleJSON
		if err := json.Unmarshal(raw, &pr); err != nil {
			dropped++
			continue
		}
		rule := Rule{
			Priority: proposedBasePrio + len(props),
			Match:    pr.Match,
			Action:   pr.Action,
			Enabled:  false,
			Source:   SourceProposed,
		}
		probe := rule
		probe.Enabled = true                 // Engine.Rules() lists enabled rules only
		eng, err := NewEngine([]Rule{probe}) // same validation as manual rules
		if err != nil {
			dropped++
			continue
		}
		norm := eng.Rules()[0]
		norm.ID, norm.Enabled, norm.Source, norm.Priority = nil, false, SourceProposed, rule.Priority
		key := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%t", norm.Match.Field, norm.Match.Op, strings.ToLower(norm.Match.Key),
			strings.ToLower(norm.Match.Value), norm.Action.Product, norm.Action.Environment, norm.Action.Shared)
		if seen[key] {
			dropped++
			continue
		}
		seen[key] = true
		if len(props) >= maxRules {
			dropped++
			continue
		}
		conf := strings.ToLower(strings.TrimSpace(pr.Confidence))
		switch conf {
		case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		default:
			conf = ConfidenceLow
		}
		rat := strings.TrimSpace(pr.Rationale)
		if len(rat) > maxNotedRationale {
			rat = rat[:maxNotedRationale]
		}
		single := norm
		single.Enabled = true
		props = append(props, Proposal{Rule: norm, Confidence: conf, Rationale: rat, Covers: countCovers(single, in.Items)})
	}
	return props, dropped, nil
}

// countCovers counts the input items the rule would attribute (ignoring
// labels, which the proposer input does not carry).
func countCovers(r Rule, items []UnattributedItem) int {
	eng, err := NewEngine([]Rule{r})
	if err != nil {
		return 0
	}
	n := 0
	for _, it := range items {
		row := Row{ProjectID: it.ProjectID, Service: it.Service, ResourceName: deref(it.ResourceName),
			ResourceGlobalName: deref(it.ResourceGlobalName), SKU: deref(it.SKU)}
		ev := eng.Evaluate(row)
		if ev.Product != "" || ev.Shared || ev.Environment != "" {
			n++
		}
	}
	return n
}
