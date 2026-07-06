package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"
)

const name = "codex"

type Src struct{ root string }

func New() *Src {
	home, _ := os.UserHomeDir()
	return &Src{root: filepath.Join(home, ".codex", "sessions")}
}

func NewWithRoot(root string) *Src { return &Src{root: root} }

func (s *Src) Name() string { return name }

func (s *Src) Detect() (bool, string) {
	entries, err := os.ReadDir(s.root)
	return err == nil && len(entries) > 0, s.root
}

func (s *Src) ScanTargets() ([]string, error) {
	return filepath.Glob(filepath.Join(s.root, "*", "*", "*", "*.jsonl"))
}

func (s *Src) WatchDirs() []string { return []string{s.root} }

type totals struct {
	Input     int64 `json:"input_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	Output    int64 `json:"output_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
	Total     int64 `json:"total_tokens"`
}

type fileMeta struct {
	SessionID string `json:"sid,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Model     string `json:"model,omitempty"`
	Prev      totals `json:"prev"`
}

type lineRec struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
}

type turnContextPayload struct {
	Model string `json:"model"`
}

type eventMsgPayload struct {
	Type string `json:"type"`
	Info *struct {
		Total *totals `json:"total_token_usage"`
		Last  *totals `json:"last_token_usage"`
	} `json:"info"`
}

var (
	markSessionMeta = []byte(`"session_meta"`)
	markTurnContext = []byte(`"turn_context"`)
	markTokenCount  = []byte(`"token_count"`)
)

func (s *Src) Parse(path string, st *source.FileState, emit func(source.UsageEvent)) (source.FileState, error) {
	var meta fileMeta
	if st.State != "" {
		_ = json.Unmarshal([]byte(st.State), &meta)
	}
	if meta.SessionID == "" {
		meta.SessionID = sessionIDFromFilename(path)
	}

	newOff, err := source.TailLines(path, st.Offset, func(line []byte) {
		isMeta := bytes.Contains(line, markSessionMeta)
		isTurn := bytes.Contains(line, markTurnContext)
		isTok := bytes.Contains(line, markTokenCount)
		if !isMeta && !isTurn && !isTok {
			return
		}
		var l lineRec
		if json.Unmarshal(line, &l) != nil {
			return
		}
		switch l.Type {
		case "session_meta":
			var p sessionMetaPayload
			if json.Unmarshal(l.Payload, &p) == nil {
				if p.ID != "" {
					meta.SessionID = p.ID
				}
				if p.CWD != "" {
					meta.CWD = p.CWD
				}
				meta.Prev = totals{}
			}
		case "turn_context":
			var p turnContextPayload
			if json.Unmarshal(l.Payload, &p) == nil && p.Model != "" {
				meta.Model = p.Model
			}
		case "event_msg":
			var p eventMsgPayload
			if json.Unmarshal(l.Payload, &p) != nil || p.Type != "token_count" || p.Info == nil {
				return
			}
			delta, cumTotal, ok := diff(&meta, p.Info.Total, p.Info.Last)
			if !ok {
				return
			}
			ts, terr := time.Parse(time.RFC3339Nano, l.Timestamp)
			if terr != nil || meta.Model == "" {
				return
			}
			emit(source.UsageEvent{
				Source:    name,
				DedupKey:  meta.SessionID + ":" + l.Timestamp + ":" + itoa(cumTotal),
				Model:     meta.Model,
				SessionID: meta.SessionID,
				Project:   meta.CWD,
				TS:        ts.UTC(),
				Input:     clamp(delta.Input - delta.Cached),
				Output:    clamp(delta.Output - delta.Reasoning),
				CacheRead: clamp(delta.Cached),
				Reasoning: clamp(delta.Reasoning),
			})
		}
	})

	ns := *st
	ns.Offset = newOff
	if b, merr := json.Marshal(meta); merr == nil {
		ns.State = string(b)
	}
	return ns, err
}

func diff(meta *fileMeta, cur, last *totals) (d totals, cumTotal int64, ok bool) {
	switch {
	case cur != nil:
		if cur.Total < meta.Prev.Total {
			meta.Prev = totals{}
		}
		d = totals{
			Input:     cur.Input - meta.Prev.Input,
			Cached:    cur.Cached - meta.Prev.Cached,
			Output:    cur.Output - meta.Prev.Output,
			Reasoning: cur.Reasoning - meta.Prev.Reasoning,
			Total:     cur.Total - meta.Prev.Total,
		}
		meta.Prev = *cur
		return d, cur.Total, d.Total > 0
	case last != nil:
		return *last, meta.Prev.Total + last.Total, last.Total > 0
	default:
		return d, 0, false
	}
}

func sessionIDFromFilename(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if len(base) >= 36 {
		return base[len(base)-36:]
	}
	return base
}

func clamp(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func itoa(v int64) string {
	var buf [20]byte
	i := len(buf)
	n := uint64(v)
	for {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	return string(buf[i:])
}
