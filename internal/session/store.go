package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/oklog/ulid/v2"
)

const (
	metaFile   = "meta.json"
	eventsFile = "events.jsonl"
	lockFile   = "lock"
	badFile    = "events.bad"
)

// TokenUsage 是会话累计 token 用量。
type TokenUsage struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

// Meta 是会话元数据。
type Meta struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Cwd           string     `json:"cwd"`
	Model         string     `json:"model"`
	Shell         string     `json:"shell"`
	Title         string     `json:"title"`
	Status        string     `json:"status"` // active | recovered | ended
	EventCount    int        `json:"event_count"`
	Tokens        TokenUsage `json:"tokens"`
	Cost          float64    `json:"cost"`
}

// ErrLocked 表示会话已被另一个进程占用。
var ErrLocked = errors.New("会话正在被另一个进程使用")

// Store 是一个会话的追加写存储。
type Store struct {
	mu   sync.Mutex
	dir  string
	f    *os.File
	lock *flock.Flock
	meta Meta
	seq  int64
}

// Create 新建会话目录并拿到排他锁。
func Create(root string, meta Meta) (*Store, error) {
	day := time.Now().Format("20060102")
	id := ulid.Make().String()
	dir := filepath.Join(root, day, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建会话目录失败: %w", err)
	}

	now := time.Now()
	meta.SchemaVersion = SchemaVersion
	meta.ID = id
	meta.CreatedAt = now
	meta.UpdatedAt = now
	meta.Status = "active"
	if meta.Title == "" {
		meta.Title = "(未命名)"
	}

	lock := flock.New(filepath.Join(dir, lockFile))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("会话加锁失败: %w", err)
	}
	if !ok {
		return nil, ErrLocked
	}

	f, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = lock.Unlock()
		return nil, err
	}

	s := &Store{dir: dir, f: f, lock: lock, meta: meta}
	if err := writeMeta(dir, meta); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// Open 打开已有会话并拿排他锁（不等待）。
func Open(dir string) (*Store, error) {
	raw, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return nil, fmt.Errorf("读取会话 meta 失败: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("解析会话 meta 失败: %w", err)
	}

	lock := flock.New(filepath.Join(dir, lockFile))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("会话加锁失败: %w", err)
	}
	if !ok {
		return nil, ErrLocked
	}

	// seq 必须从事件文件推导，不能信 meta.EventCount：
	// 崩在 Sync 之前时 meta 会落后，用它起 seq 会写出重复编号。
	seq, err := lastEventSeq(dir)
	if err != nil {
		_ = lock.Unlock()
		return nil, err
	}

	f, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return &Store{dir: dir, f: f, lock: lock, meta: meta, seq: seq}, nil
}

// lastEventSeq 返回事件文件里最后一个完整事件的 seq。
// 文件尾部不完整（没有以换行结尾）时拒绝打开，避免追加到半行后面。
func lastEventSeq(dir string) (int64, error) {
	path := filepath.Join(dir, eventsFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		return 0, fmt.Errorf("会话事件文件尾部不完整，请先执行恢复")
	}
	var seq int64
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) != nil {
			break
		}
		seq = e.Seq
	}
	return seq, nil
}

// Dir 返回会话目录。
func (s *Store) Dir() string { return s.dir }

// Meta 返回元数据副本。
func (s *Store) Meta() Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// SetStatus 更新状态并落盘。
func (s *Store) SetStatus(status string) error {
	s.mu.Lock()
	s.meta.Status = status
	s.mu.Unlock()
	return s.Sync()
}

// Append 追加一个事件，自动分配 seq 与时间戳。
func (s *Store) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e.V = SchemaVersion
	e.Seq = s.seq + 1
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	if err := e.Validate(); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("编码事件失败: %w", err)
	}
	if _, err := s.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("写入事件失败: %w", err)
	}
	s.seq = e.Seq
	s.meta.EventCount = int(s.seq)
	s.meta.UpdatedAt = e.TS
	// 恢复后新的一轮开始，状态回到 active，避免会话永远停在 recovered。
	if e.Type == TypeTurnStarted {
		s.meta.Status = "active"
	}

	if e.Type == TypeTurnStarted && s.meta.Title == "(未命名)" {
		var t TurnStarted
		if e.Decode(&t) == nil {
			s.meta.Title = titleOf(t.UserText)
		}
	}
	return nil
}

// Record 实现 agent.Recorder：追加事件。
func (s *Store) Record(e Event) error { return s.Append(e) }

// AddUsage 累计一次模型调用的 token 与成本；随下一次 Sync 落盘。
func (s *Store) AddUsage(in, out int, cost float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta.Tokens.In += in
	s.meta.Tokens.Out += out
	s.meta.Cost += cost
	return nil
}

// Sync 刷盘并原子更新 meta。
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.f.Sync(); err != nil {
		return err
	}
	return writeMeta(s.dir, s.meta)
}

// SaveSummary 写入摘要事件并刷新 summary.md 视图。
func (s *Store) SaveSummary(text string, covered int) error {
	ev, err := New(TypeSummary, Summary{Text: text, Covered: covered, At: time.Now()})
	if err != nil {
		return err
	}
	if err := s.Append(ev); err != nil {
		return err
	}
	path := filepath.Join(s.dir, "summary.md")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return s.Sync()
}

// SaveTruncated 记录没有摘要的硬截断，保证重启后按同一覆盖数恢复。
func (s *Store) SaveTruncated(covered int) error {
	ev, err := New(TypeTruncated, Truncated{Covered: covered, At: time.Now()})
	if err != nil {
		return err
	}
	if err := s.Append(ev); err != nil {
		return err
	}
	return s.Sync()
}

// Close 刷盘并释放锁。
func (s *Store) Close() error {
	err := s.Sync()
	_ = s.f.Close()
	_ = s.lock.Unlock()
	return err
}

// List 列出 root 下所有会话的 meta，按更新时间倒序。
func List(root string) ([]Meta, error) {
	var out []Meta
	days, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		ids, err := os.ReadDir(filepath.Join(root, day.Name()))
		if err != nil {
			continue
		}
		for _, id := range ids {
			if !id.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, day.Name(), id.Name(), metaFile))
			if err != nil {
				continue
			}
			var m Meta
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Find 按 id（或 id 前缀）查找会话目录。
func Find(root, idOrPrefix string) (string, error) {
	metas, err := List(root)
	if err != nil {
		return "", err
	}
	var matches []Meta
	for _, m := range metas {
		if m.ID == idOrPrefix || (len(idOrPrefix) >= 4 && len(m.ID) > len(idOrPrefix) && m.ID[:len(idOrPrefix)] == idOrPrefix) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("找不到会话 %q", idOrPrefix)
	case 1:
		metas = matches
	default:
		return "", fmt.Errorf("会话前缀 %q 匹配到 %d 个，请写得更长", idOrPrefix, len(matches))
	}
	return sessionDir(root, metas[0]), nil
}

func sessionDir(root string, m Meta) string {
	return filepath.Join(root, m.CreatedAt.Format("20060102"), m.ID)
}

// EventsPath 返回会话事件文件路径。
func EventsPath(dir string) string { return filepath.Join(dir, eventsFile) }

func writeMeta(dir string, meta Meta) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, metaFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, metaFile))
}

func titleOf(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "(未命名)"
	}
	if len(r) > 30 {
		return string(r[:30]) + "…"
	}
	return string(r)
}
