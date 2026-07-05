// Package pricing maps model names to per-token costs using the LiteLLM
// community pricing dataset. Resolution order: fresh local cache (<24h) →
// network fetch → stale cache → embedded snapshot. Load never fails.
package pricing

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed snapshot.json
var snapshot []byte

const (
	// URL is the canonical LiteLLM pricing dataset.
	URL      = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	cacheTTL = 24 * time.Hour
)

// ModelPrice holds per-token USD costs for one model.
type ModelPrice struct {
	InputCostPerToken           float64 `json:"input_cost_per_token"`
	OutputCostPerToken          float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost float64 `json:"cache_creation_input_token_cost"`
}

// Table is a loaded pricing table with memoized fuzzy matching.
type Table struct {
	prices map[string]ModelPrice
	norm   map[string]string // normalized key -> canonical key
	memo   map[string]string // raw model name -> canonical key ("" = miss)

	Source    string // "cache" | "network" | "stale-cache" | "embedded"
	FetchedAt time.Time
	Count     int
}

// CachePath returns where the pricing cache lives: alongside TOCY_DB if set,
// otherwise ~/.tocy/pricing.json.
func CachePath() string {
	if db := os.Getenv("TOCY_DB"); db != "" {
		return filepath.Join(filepath.Dir(db), "pricing.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "tocy-pricing.json")
	}
	return filepath.Join(home, ".tocy", "pricing.json")
}

// Load returns a usable pricing table, never an error. force skips the
// cache-freshness shortcut and always attempts a network refresh.
func Load(force bool) *Table {
	path := CachePath()

	if !force {
		if t := fromFile(path, cacheTTL); t != nil {
			t.Source = "cache"
			return t
		}
	}

	if body, err := fetch(); err == nil {
		if t := parse(body); t != nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, body, 0o644)
			t.Source = "network"
			t.FetchedAt = time.Now()
			return t
		}
	}

	// Network failed: any cache, even stale, beats the embedded snapshot.
	if t := fromFile(path, 0); t != nil {
		t.Source = "stale-cache"
		return t
	}

	t := parse(snapshot)
	if t == nil {
		// Corrupt embed is a build bug; return an empty (all-miss) table
		// rather than panicking in a TUI.
		t = &Table{prices: map[string]ModelPrice{}, norm: map[string]string{}, memo: map[string]string{}}
	}
	t.Source = "embedded"
	return t
}

// fromFile parses path if it exists and (when ttl > 0) is younger than ttl.
func fromFile(path string, ttl time.Duration) *Table {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if ttl > 0 && time.Since(fi.ModTime()) > ttl {
		return nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	t := parse(body)
	if t != nil {
		t.FetchedAt = fi.ModTime()
	}
	return t
}

func fetch() ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

type httpError struct{ code int }

func (e *httpError) Error() string { return "pricing fetch: HTTP " + http.StatusText(e.code) }

func parse(body []byte) *Table {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	t := &Table{
		prices: make(map[string]ModelPrice, len(raw)),
		norm:   make(map[string]string, len(raw)),
		memo:   map[string]string{},
	}
	for k, v := range raw {
		if k == "sample_spec" {
			continue // documentation entry in the LiteLLM file
		}
		var p ModelPrice
		if err := json.Unmarshal(v, &p); err != nil {
			continue
		}
		if p.InputCostPerToken == 0 && p.OutputCostPerToken == 0 &&
			p.CacheReadInputTokenCost == 0 && p.CacheCreationInputTokenCost == 0 {
			continue // free/embedding/unpriced entries can't help us
		}
		t.prices[k] = p
		n := normalize(k)
		// Prefer the shortest canonical key per normalized form (bare
		// "claude-sonnet-5" over "anthropic/claude-sonnet-5").
		if prev, ok := t.norm[n]; !ok || len(k) < len(prev) {
			t.norm[n] = k
		}
	}
	t.Count = len(t.prices)
	return t
}

var dateSuffix = regexp.MustCompile(`[-@]20\d{6}$`)

// normalize collapses naming variants: lowercase, "." → "-" in version
// numbers is NOT done globally (gpt-5.5 is canonical), but date stamps,
// "-latest" and "-free" suffixes are stripped.
func normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = dateSuffix.ReplaceAllString(n, "")
	n = strings.TrimSuffix(n, "-latest")
	n = strings.TrimSuffix(n, ":free")
	n = strings.TrimSuffix(n, "-free")
	return n
}

// candidates yields lookup keys derived from a raw model name, most
// specific first.
func candidates(model string) []string {
	m := strings.TrimSpace(model)
	out := []string{m, strings.ToLower(m)}
	// Provider-qualified names: try progressively shorter tails.
	// "anthropic/pioneer/deepseek-ai/DeepSeek-V4-Pro" →
	// "pioneer/deepseek-ai/DeepSeek-V4-Pro", "deepseek-ai/DeepSeek-V4-Pro", "DeepSeek-V4-Pro"
	parts := strings.Split(m, "/")
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[i:], "/"))
	}
	// Bare name: try common provider prefixes as LiteLLM sometimes only
	// has the qualified form.
	if !strings.Contains(m, "/") {
		for _, p := range []string{"anthropic/", "openai/", "gemini/", "vertex_ai/", "openrouter/"} {
			out = append(out, p+m)
		}
	}
	// "." vs "-" version separators: claude-opus-4.7 ↔ claude-opus-4-7.
	if strings.Contains(m, ".") {
		out = append(out, strings.ReplaceAll(m, ".", "-"))
	}
	if strings.Contains(m, "-") {
		// only swap the LAST "-" between digits to avoid mangling names
		if alt := dotAlt(m); alt != "" {
			out = append(out, alt)
		}
	}
	return out
}

var dashVersion = regexp.MustCompile(`(\d)-(\d+)$`)

func dotAlt(m string) string {
	if dashVersion.MatchString(m) {
		return dashVersion.ReplaceAllString(m, "$1.$2")
	}
	return ""
}

// Match resolves a raw model name to a price. Results (hits and misses)
// are memoized.
func (t *Table) Match(model string) (ModelPrice, bool) {
	if model == "" {
		return ModelPrice{}, false
	}
	if key, seen := t.memo[model]; seen {
		if key == "" {
			return ModelPrice{}, false
		}
		return t.prices[key], true
	}
	key := t.resolve(model)
	t.memo[model] = key
	if key == "" {
		return ModelPrice{}, false
	}
	return t.prices[key], true
}

func (t *Table) resolve(model string) string {
	for _, c := range candidates(model) {
		if _, ok := t.prices[c]; ok {
			return c
		}
	}
	for _, c := range candidates(model) {
		if k, ok := t.norm[normalize(c)]; ok {
			return k
		}
	}
	return ""
}

// Cost prices a token bundle. ok=false means the model is unknown and the
// caller must surface it as unpriced — never as $0.
func (t *Table) Cost(model string, input, output, cacheRead, cacheWrite int64) (usd float64, ok bool) {
	p, ok := t.Match(model)
	if !ok {
		return 0, false
	}
	return float64(input)*p.InputCostPerToken +
		float64(output)*p.OutputCostPerToken +
		float64(cacheRead)*p.CacheReadInputTokenCost +
		float64(cacheWrite)*p.CacheCreationInputTokenCost, true
}
