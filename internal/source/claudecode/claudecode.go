// Package claudecode parses Claude Code session transcripts:
// ~/.claude/projects/<dash-encoded-cwd>/<session>.jsonl
package claudecode

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"
)

const name = "claude-code"

type Src struct{ root string }

// New returns the source rooted at ~/.claude/projects.
func New() *Src {
	home, _ := os.UserHomeDir()
	return &Src{root: filepath.Join(home, ".claude", "projects")}
}

// NewWithRoot is used by tests and fixtures.
func NewWithRoot(root string) *Src { return &Src{root: root} }

func (s *Src) Name() string { return name }

func (s *Src) Detect() (bool, string) {
	entries, err := os.ReadDir(s.root)
	return err == nil && len(entries) > 0, s.root
}

func (s *Src) ScanTargets() ([]string, error) {
	return filepath.Glob(filepath.Join(s.root, "*", "*.jsonl"))
}

func (s *Src) WatchDirs() []string { return []string{s.root} }

// lineRec is the subset of a transcript line we care about.
type lineRec struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var assistantMarker = []byte(`"assistant"`)

func (s *Src) Parse(path string, st *source.FileState, emit func(source.UsageEvent)) (source.FileState, error) {
	newOff, err := source.TailLines(path, st.Offset, func(line []byte) {
		// Fast pre-filter: skip user/summary/system lines without unmarshalling.
		if !bytes.Contains(line, assistantMarker) {
			return
		}
		var l lineRec
		if json.Unmarshal(line, &l) != nil {
			return
		}
		if l.Type != "assistant" || l.Message.Usage == nil {
			return
		}
		model := l.Message.Model
		if model == "" || model == "<synthetic>" {
			return
		}
		ts, terr := time.Parse(time.RFC3339Nano, l.Timestamp)
		if terr != nil {
			return
		}
		u := l.Message.Usage
		emit(source.UsageEvent{
			Source:     name,
			DedupKey:   dedupKey(l),
			Model:      model,
			SessionID:  l.SessionID,
			Project:    l.CWD,
			TS:         ts.UTC(),
			Input:      u.InputTokens,
			Output:     u.OutputTokens,
			CacheRead:  u.CacheReadInputTokens,
			CacheWrite: u.CacheCreationInputTokens,
		})
	})
	ns := *st
	ns.Offset = newOff
	return ns, err
}

// dedupKey: streamed/retried lines repeat the same API message — key on
// message.id+requestId (ccusage-compatible); fall back to message.id alone
// (conservative: never double-counts), then line uuid.
func dedupKey(l lineRec) string {
	switch {
	case l.Message.ID != "" && l.RequestID != "":
		return l.Message.ID + ":" + l.RequestID
	case l.Message.ID != "":
		return l.Message.ID
	default:
		return l.UUID
	}
}
