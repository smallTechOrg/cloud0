package attribution

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed prompts/proposer_system.txt prompts/proposer_user.txt
var proposerPromptFS embed.FS

const (
	// maxProposeResources caps how many unattributed items reach the model.
	maxProposeResources = 150
	// maxExistingRulesInPrompt caps the existing-rule context.
	maxExistingRulesInPrompt = 100
	maxPromptField           = 160
	maxPromptEvidence        = 4
)

func mustPrompt(name string) string {
	b, err := proposerPromptFS.ReadFile("prompts/" + name)
	if err != nil {
		panic("attribution: missing embedded prompt " + name) // build-time bug
	}
	return strings.TrimSpace(string(b))
}

type promptResource struct {
	Rank     int      `json:"rank"`
	Kind     string   `json:"kind"`
	Project  string   `json:"project,omitempty"`
	Service  string   `json:"service,omitempty"`
	Resource string   `json:"resource,omitempty"`
	SKU      string   `json:"sku,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

type promptRule struct {
	Field       string `json:"field"`
	Op          string `json:"op"`
	Key         string `json:"key,omitempty"`
	Value       string `json:"value"`
	Product     string `json:"product,omitempty"`
	Environment string `json:"environment,omitempty"`
	Shared      bool   `json:"shared,omitempty"`
}

func clip(s string) string {
	if len(s) > maxPromptField {
		return s[:maxPromptField]
	}
	return s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// buildProposerPrompt returns the system and user messages. It is
// deterministic for equal input, includes at most maxProposeResources items
// (ranked by Net descending, input order for ties; only the rank is sent,
// never the amount) and no secrets.
func buildProposerPrompt(in ProposeInput, maxRules int) (system, user string) {
	items := append([]UnattributedItem(nil), in.Items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Net > items[j].Net })
	if len(items) > maxProposeResources {
		items = items[:maxProposeResources]
	}
	var res strings.Builder
	for i, it := range items {
		pr := promptResource{
			Rank: i + 1, Kind: it.Kind, Project: clip(it.ProjectID), Service: clip(it.Service),
			Resource: clip(deref(it.ResourceName)), SKU: clip(deref(it.SKU)),
		}
		if pr.Resource == "" {
			pr.Resource = clip(deref(it.ResourceGlobalName))
		}
		for _, e := range it.Evidence {
			if len(pr.Evidence) == maxPromptEvidence {
				break
			}
			pr.Evidence = append(pr.Evidence, clip(e))
		}
		b, _ := json.Marshal(pr)
		res.Write(b)
		res.WriteByte('\n')
	}
	var ex strings.Builder
	n := 0
	for _, r := range in.Existing {
		if !r.Enabled || n == maxExistingRulesInPrompt {
			continue
		}
		n++
		b, _ := json.Marshal(promptRule{
			Field: r.Match.Field, Op: r.Match.Op, Key: clip(r.Match.Key), Value: clip(r.Match.Value),
			Product: r.Action.Product, Environment: r.Action.Environment, Shared: r.Action.Shared,
		})
		ex.Write(b)
		ex.WriteByte('\n')
	}
	if n == 0 {
		ex.WriteString("(none)\n")
	}
	if res.Len() == 0 {
		res.WriteString("(none)\n")
	}
	user = mustPrompt("proposer_user.txt")
	user = strings.NewReplacer(
		"{{MAX_RULES}}", fmt.Sprint(maxRules),
		"{{EXISTING}}", strings.TrimRight(ex.String(), "\n"),
		"{{RESOURCES}}", strings.TrimRight(res.String(), "\n"),
	).Replace(user)
	return mustPrompt("proposer_system.txt"), user
}
