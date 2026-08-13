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
	"sync"
	"time"
)

//go:embed snapshot.json
var snapshot []byte

const (
	URL      = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	cacheTTL = 24 * time.Hour
)

type ModelPrice struct {
	InputCostPerToken           float64 `json:"input_cost_per_token"`
	OutputCostPerToken          float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost float64 `json:"cache_creation_input_token_cost"`
	OutputCostPerReasoningToken float64 `json:"output_cost_per_reasoning_token"`
}

type Table struct {
	prices map[string]ModelPrice
	norm   map[string]string

	// memo caches model-name resolution; guarded by mu because Match is
	// called from concurrent TUI load commands.
	mu   sync.Mutex
	memo map[string]string

	Source    string
	FetchedAt time.Time
	Count     int
}

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
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			if filepath.Base(filepath.Dir(path)) == ".tocy" {
				_ = os.Chmod(filepath.Dir(path), 0o700)
			}
			_ = os.WriteFile(path, body, 0o600)
			_ = os.Chmod(path, 0o600)
			t.Source = "network"
			t.FetchedAt = time.Now()
			return t
		}
	}

	if t := fromFile(path, 0); t != nil {
		t.Source = "stale-cache"
		return t
	}

	t := parse(snapshot)
	if t == nil {
		t = &Table{
			prices: map[string]ModelPrice{},
			norm:   map[string]string{},
			memo:   map[string]string{},
		}
	}
	t.Source = "embedded"
	return t
}

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
	defer func() { _ = resp.Body.Close() }()
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
			continue
		}
		var p ModelPrice
		if err := json.Unmarshal(v, &p); err != nil {
			continue
		}
		if p.InputCostPerToken == 0 && p.OutputCostPerToken == 0 &&
			p.CacheReadInputTokenCost == 0 && p.CacheCreationInputTokenCost == 0 {
			continue
		}
		t.prices[k] = p
		n := normalize(k)
		if prev, ok := t.norm[n]; !ok || len(k) < len(prev) {
			t.norm[n] = k
		}
	}
	t.Count = len(t.prices)
	return t
}

var dateSuffix = regexp.MustCompile(`[-@]20\d{6}$`)

func normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = dateSuffix.ReplaceAllString(n, "")
	n = strings.TrimSuffix(n, "-latest")
	n = strings.TrimSuffix(n, ":free")
	n = strings.TrimSuffix(n, "-free")
	return n
}

func candidates(model string) []string {
	m := strings.TrimSpace(model)
	out := []string{m, strings.ToLower(m)}
	parts := strings.Split(m, "/")
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[i:], "/"))
	}
	if !strings.Contains(m, "/") {
		for _, p := range []string{"anthropic/", "openai/", "gemini/", "vertex_ai/", "openrouter/"} {
			out = append(out, p+m)
		}
	}
	if strings.Contains(m, ".") {
		out = append(out, strings.ReplaceAll(m, ".", "-"))
	}
	if strings.Contains(m, "-") {
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

func (t *Table) Match(model string) (ModelPrice, bool) {
	if model == "" {
		return ModelPrice{}, false
	}
	t.mu.Lock()
	key, seen := t.memo[model]
	if !seen {
		key = t.resolve(model)
		t.memo[model] = key
	}
	t.mu.Unlock()
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

func (t *Table) Cost(model string, input, output, cacheRead, cacheWrite, reasoning int64) (usd float64, ok bool) {
	p, ok := t.Match(model)
	if !ok {
		return 0, false
	}
	reasoningRate := p.OutputCostPerReasoningToken
	if reasoningRate == 0 {
		reasoningRate = p.OutputCostPerToken
	}
	return float64(input)*p.InputCostPerToken +
		float64(output)*p.OutputCostPerToken +
		float64(cacheRead)*p.CacheReadInputTokenCost +
		float64(cacheWrite)*p.CacheCreationInputTokenCost +
		float64(reasoning)*reasoningRate, true
}
