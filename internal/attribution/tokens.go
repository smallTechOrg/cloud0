package attribution

import (
	"sort"
	"strings"
	"unicode"
)

// seedStopTokens are generic resource-name tokens that describe a kind of thing,
// an environment-neutral role or a location, never a product. The seeder never
// proposes a product from one of them (an explicit alias still can).
var seedStopTokens = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`
vm vms instance instances projects project backend frontend storefront web api db bucket buckets media
backups backup bak cache migrate migration jobs job repo repos ip ips lb default prod staging dev beta test
us eu central central1 europe asia west east north south zone zones region regions global regional
svc service services srv server servers app apps worker workers queue topic sub subscription
function functions cluster node nodes pool main primary replica master slave data logs log metrics monitoring
uploads static assets public private internal external disk disks boot image images snapshot snapshots
network networks subnet subnetwork vpc firewall router nat gateway gce gke gcs sql run redis postgres
postgresql mysql address addresses load balancer forwarding rule rules proxy nginx docker registry
artifact artifacts container containers compute engine storage tmp temp old new copy template templates
group groups health check admin user users scheduler cron task tasks etl batch stream http https tcp udp
ssl cert tls dns key keys secret secrets tf terraform ci cd build builds deploy release latest stable canary
mgmt ops infra tools tool bastion vpn ssh jump dr google googleapis gcr appspot cloud cloudrun
the and for to of in on at www
`) {
		seedStopTokens[t] = true
	}
}

// StopTokens returns the sorted stop-list: generic tokens that never become a
// product. Environment tokens (prod, staging, ...) are in the list too.
func StopTokens() []string {
	out := make([]string, 0, len(seedStopTokens))
	for t := range seedStopTokens {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// IsStopToken reports whether tok (case-insensitive) is on the stop-list.
func IsStopToken(tok string) bool { return seedStopTokens[strings.ToLower(tok)] }

// seedEnvSynonyms maps an environment name to the name tokens that denote it.
var seedEnvOrder = []string{"prod", "staging", "dev", "beta", "test", "qa"}

var seedEnvSynonyms = map[string][]string{
	"prod":    {"prod", "production"},
	"staging": {"staging", "stage"},
	"dev":     {"dev", "development"},
	"beta":    {"beta"},
	"test":    {"test"},
	"qa":      {"qa"},
}

var seedEnvByToken = func() map[string]string {
	m := map[string]string{}
	for env, toks := range seedEnvSynonyms {
		for _, t := range toks {
			m[t] = env
		}
	}
	return m
}()

// EnvForToken maps an environment token (prod, production, stage, ...) to its
// canonical environment name.
func EnvForToken(tok string) (string, bool) {
	e, ok := seedEnvByToken[strings.ToLower(tok)]
	return e, ok
}

// Tokenize lower-cases name and splits it on every non-alphanumeric rune and on
// letter/digit boundaries; digit runs are dropped ("vm2-web" -> vm, web).
// Order is preserved and duplicates kept (adjacency matters to the seeder).
func Tokenize(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) {
			cur = append(cur, r)
			continue
		}
		flush() // digits, separators and everything else end a token
	}
	flush()
	return out
}
