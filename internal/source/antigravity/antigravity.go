// Package antigravity ingests Google's Antigravity CLI usage from its
// per-conversation SQLite databases under ~/.gemini/antigravity-cli/.
//
// Format notes (reverse-engineered 2026-07-28, no public schema):
//   - conversations/<uuid>.db, one per conversation, WAL mode. The uuid is
//     the conversation/trajectory id and doubles as our session id.
//   - steps(idx PRIMARY KEY, metadata BLOB, ...): metadata is a protobuf.
//     Top-level field 1 is a Timestamp{1:seconds}, field 9 is the per-request
//     LLM usage submessage:
//     9.1 model id (enum)   9.2 input tokens      9.3 output tokens (total)
//     9.5 cache-read tokens 9.9+9.10 output split 9.11 unique generation id
//     Verified across 15,994 records: 9.3 == 9.9 + 9.10 always, and 9.11 is
//     globally unique — it is our dedup key. 9.9/9.10 attribution (reasoning
//     vs visible) is unconfirmed, so we report only the total as Output.
//   - gen_metadata(idx, data BLOB): data field 3.15.1 is the model id and
//     3.28 its display name (e.g. "gemini-3.6-flash-high").
//   - trajectory_metadata_blob: single row; data field 1.1 is the workspace
//     file:// URI (our project).
//
// Older <uuid>.pb conversation files predate the sqlite format and are not
// parsed. All decoding is defensive: unparseable blobs are skipped, never
// fatal, since the format is undocumented and may change.
package antigravity

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"

	_ "modernc.org/sqlite"
)

const name = "antigravity"

type Src struct {
	root string
	// names accumulates the model id → display-name mapping across all
	// conversation dbs in this scan: ids are globally consistent, but not
	// every db's gen_metadata covers every id its steps reference. Parse
	// is called sequentially per source, so no locking is needed.
	names map[uint64]string
}

func New() *Src {
	home, _ := os.UserHomeDir()
	return NewWithRoot(filepath.Join(home, ".gemini", "antigravity-cli", "conversations"))
}

func NewWithRoot(root string) *Src {
	return &Src{root: root, names: map[uint64]string{}}
}

func (s *Src) Name() string { return name }

func (s *Src) Detect() (bool, string) {
	m, err := filepath.Glob(filepath.Join(s.root, "*.db"))
	return err == nil && len(m) > 0, s.root
}

func (s *Src) ScanTargets() ([]string, error) {
	return filepath.Glob(filepath.Join(s.root, "*.db"))
}

// AlwaysScan: change detection is done inside Parse against both the db and
// its -wal sidecar, since WAL commits don't touch the main file's mtime.
func (s *Src) AlwaysScan() bool { return true }

type cursorState struct {
	LastIdx    int64 `json:"last_idx"`
	DBSize     int64 `json:"db_size,omitempty"`
	DBMtimeNS  int64 `json:"db_mtime_ns,omitempty"`
	WalSize    int64 `json:"wal_size,omitempty"`
	WalMtimeNS int64 `json:"wal_mtime_ns,omitempty"`
}

func statOf(path string) (int64, int64) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return fi.Size(), fi.ModTime().UnixNano()
}

func (s *Src) Parse(path string, st *source.FileState, emit func(source.UsageEvent)) (source.FileState, error) {
	ns := *st
	ns.Offset = 0

	var cur cursorState
	if st.State != "" {
		_ = json.Unmarshal([]byte(st.State), &cur)
	}

	dbSize, dbMt := statOf(path)
	walSize, walMt := statOf(path + "-wal")
	if st.State != "" &&
		cur.DBSize == dbSize && cur.DBMtimeNS == dbMt &&
		cur.WalSize == walSize && cur.WalMtimeNS == walMt {
		return ns, nil
	}

	db, err := sql.Open("sqlite", source.SQLiteDSN(path, "mode=ro"))
	if err != nil {
		return ns, err
	}
	defer func() { _ = db.Close() }()

	sessionID := strings.TrimSuffix(filepath.Base(path), ".db")
	for id, nm := range modelNames(db) {
		s.names[id] = nm
	}
	project := projectOf(db)

	rows, err := db.Query(
		`SELECT idx, metadata FROM steps
		 WHERE idx > ? AND metadata IS NOT NULL ORDER BY idx`, cur.LastIdx)
	if err != nil {
		return ns, err
	}
	defer func() { _ = rows.Close() }()

	maxIdx := cur.LastIdx
	for rows.Next() {
		var (
			idx  int64
			meta []byte
		)
		if err := rows.Scan(&idx, &meta); err != nil {
			return ns, err
		}
		if idx > maxIdx {
			maxIdx = idx
		}
		u, ts, ok := decodeStep(meta)
		if !ok || u.input+u.output+u.cacheRead == 0 {
			continue
		}
		key := u.genID
		if key == "" {
			key = sessionID + ":" + itoa(idx)
		}
		model := s.names[u.modelID]
		if model == "" && u.modelID != 0 {
			// Stable placeholder so unmapped ids stay identifiable
			// instead of blending into one blank "unknown" row.
			model = "antigravity-" + itoa(int64(u.modelID))
		}
		emit(source.UsageEvent{
			Source:    name,
			DedupKey:  key,
			Model:     model,
			SessionID: sessionID,
			Project:   project,
			TS:        ts,
			Input:     u.input,
			Output:    u.output,
			CacheRead: u.cacheRead,
		})
	}
	if err := rows.Err(); err != nil {
		return ns, err
	}

	nc := cursorState{
		LastIdx: maxIdx,
		DBSize:  dbSize, DBMtimeNS: dbMt, WalSize: walSize, WalMtimeNS: walMt,
	}
	b, merr := json.Marshal(nc)
	if merr != nil {
		return ns, fmt.Errorf("marshal parser state: %w", merr)
	}
	ns.State = string(b)
	return ns, nil
}

type stepUsage struct {
	modelID   uint64
	input     int64
	output    int64
	cacheRead int64
	genID     string
}

// decodeStep extracts the usage submessage (field 9) and the step timestamp
// (field 1) from a steps.metadata protobuf blob.
func decodeStep(meta []byte) (u stepUsage, ts time.Time, ok bool) {
	for i := 0; i < len(meta); {
		f, ni, valid := pbNext(meta, i)
		if !valid {
			return u, ts, false
		}
		i = ni
		switch {
		case f.num == 1 && f.wire == 2: // Timestamp{1:seconds,2:nanos}
			if secs, found := pbVarint(f.data, 1); found && secs > 0 {
				ts = time.Unix(int64(secs), 0).UTC()
			}
		case f.num == 9 && f.wire == 2:
			for j := 0; j < len(f.data); {
				sf, nj, v := pbNext(f.data, j)
				if !v {
					return u, ts, false
				}
				j = nj
				switch sf.num {
				case 1:
					u.modelID = sf.val
				case 2:
					u.input = int64(sf.val)
				case 3:
					u.output = int64(sf.val)
				case 5:
					u.cacheRead = int64(sf.val)
				case 11:
					if sf.wire == 2 {
						u.genID = string(sf.data)
					}
				}
			}
			ok = true
		}
	}
	if ts.IsZero() {
		ok = false
	}
	return u, ts, ok
}

// modelNames maps Antigravity's numeric model ids to display names using the
// gen_metadata table (data field 3.15.1 = id, 3.28 = name).
func modelNames(db *sql.DB) map[uint64]string {
	out := map[uint64]string{}
	rows, err := db.Query(`SELECT data FROM gen_metadata`)
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			continue
		}
		var id uint64
		var nm string
		f3, found := pbSub(data, 3)
		if !found {
			continue
		}
		if f15, ok := pbSub(f3, 15); ok {
			id, _ = pbVarint(f15, 1)
		}
		if f28, ok := pbSub(f3, 28); ok {
			nm = string(f28)
		}
		if id != 0 && nm != "" {
			out[id] = nm
		}
	}
	return out
}

// projectOf reads the workspace URI from trajectory_metadata_blob (field 1.1).
func projectOf(db *sql.DB) string {
	var data []byte
	if db.QueryRow(`SELECT data FROM trajectory_metadata_blob LIMIT 1`).Scan(&data) != nil {
		return ""
	}
	f1, ok := pbSub(data, 1)
	if !ok {
		return ""
	}
	uri, ok := pbSub(f1, 1)
	if !ok {
		return ""
	}
	return strings.TrimPrefix(string(uri), "file://")
}

// --- minimal protobuf wire-format reader (stdlib only) ---

type pbField struct {
	num  int
	wire int
	val  uint64 // varint / fixed values
	data []byte // length-delimited payload
}

// pbNext decodes one field starting at offset i.
func pbNext(b []byte, i int) (pbField, int, bool) {
	tag, n := binary.Uvarint(b[i:])
	if n <= 0 {
		return pbField{}, i, false
	}
	i += n
	f := pbField{num: int(tag >> 3), wire: int(tag & 7)}
	if f.num == 0 {
		return pbField{}, i, false
	}
	switch f.wire {
	case 0: // varint
		v, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return pbField{}, i, false
		}
		f.val, i = v, i+n
	case 1: // fixed64
		if i+8 > len(b) {
			return pbField{}, i, false
		}
		f.val, i = binary.LittleEndian.Uint64(b[i:]), i+8
	case 2: // length-delimited
		l, n := binary.Uvarint(b[i:])
		if n <= 0 || uint64(len(b)-i-n) < l {
			return pbField{}, i, false
		}
		i += n
		f.data, i = b[i:i+int(l)], i+int(l)
	case 5: // fixed32
		if i+4 > len(b) {
			return pbField{}, i, false
		}
		f.val, i = uint64(binary.LittleEndian.Uint32(b[i:])), i+4
	default:
		return pbField{}, i, false
	}
	return f, i, true
}

// pbSub returns the payload of the first length-delimited field `num`.
func pbSub(b []byte, num int) ([]byte, bool) {
	for i := 0; i < len(b); {
		f, ni, ok := pbNext(b, i)
		if !ok {
			return nil, false
		}
		if f.num == num && f.wire == 2 {
			return f.data, true
		}
		i = ni
	}
	return nil, false
}

// pbVarint returns the first varint field `num`.
func pbVarint(b []byte, num int) (uint64, bool) {
	for i := 0; i < len(b); {
		f, ni, ok := pbNext(b, i)
		if !ok {
			return 0, false
		}
		if f.num == num && f.wire == 0 {
			return f.val, true
		}
		i = ni
	}
	return 0, false
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
