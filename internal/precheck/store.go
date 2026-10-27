package precheck

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PolicyVersion invalidates decisions made by the previous permissive classifier.
const PolicyVersion = 4

// Record 是记录库里的一行。
//
// 命中判据是 Path + Size + MTimeUnix 三者全同：只要用户替换了同名文件，
// size 或 mtime 必然变，就会重新判定。
type Record struct {
	PolicyVersion int      `json:"policy_version"`
	Path          string   `json:"path"`
	Size          int64    `json:"size"`
	MTime         int64    `json:"mtime"` // unix 秒
	OK            bool     `json:"ok"`
	FillTop       bool     `json:"fill_top,omitempty"`
	Reason        string   `json:"reason"`
	Tags          []string `json:"tags"`
	At            string   `json:"at"` // RFC3339
}

// Store 是 precheck.jsonl 记录库。启动时一次性读入内存 map，
// 之后只在有新结论时追加一行，从不全量重写。
type Store struct {
	mu   sync.Mutex
	path string
	idx  map[string]Record // key 见 keyOf
	file *os.File          // 懒打开，追加用
}

// NewStore 打开（或创建）指定路径的 jsonl 记录库。
//
// 文件不存在不是错误：首次运行本来就没有记录。
// 单行损坏（手工编辑、断电写一半）只跳过那一行，不拖垮整个库。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, idx: map[string]Record{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// 单行可能较长（长路径 + 中文理由），放宽到 1MB
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // 坏行跳过
		}
		if r.Path == "" || r.PolicyVersion != PolicyVersion {
			continue
		}
		s.idx[keyOf(r.Path, r.Size, r.MTime)] = r
	}
	// 扫描出错（文件被截断等）不致命：已有记录仍然可用
	return s, nil
}

// Path 返回记录库文件路径。
func (s *Store) Path() string { return s.path }

// keyOf 拼出命中键。用分隔符避免不同组合撞在一起。
func keyOf(path string, size, mtime int64) string {
	return fmt.Sprintf("%s\x00%d\x00%d", path, size, mtime)
}

// Lookup 查一条记录。stat 是调用方对文件的 os.Stat 结果。
func (s *Store) Lookup(path string, size, mtime int64) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.idx[keyOf(path, size, mtime)]
	return r, ok
}

// Append 落盘一条记录并更新内存索引。
//
// 写盘失败不影响判定结果（内存索引已经更新），但要如实返回错误让上层记警告。
func (s *Store) Append(r Record) error {
	if r.PolicyVersion == 0 {
		r.PolicyVersion = PolicyVersion
	}
	if r.At == "" {
		r.At = time.Now().Format(time.RFC3339)
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}

	s.mu.Lock()
	s.idx[keyOf(r.Path, r.Size, r.MTime)] = r
	f, err := s.openLocked()
	s.mu.Unlock()

	if err != nil {
		return err
	}

	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("追加 %s: %w", s.path, err)
	}
	return nil
}

// openLocked 懒打开追加句柄。调用方须持锁。
func (s *Store) openLocked() (*os.File, error) {
	if s.file != nil {
		return s.file, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return nil, fmt.Errorf("创建记录库目录: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w", s.path, err)
	}
	s.file = f
	return f, nil
}

// Close 关闭文件句柄。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Len 返回内存里的记录条数（含历史版本，同名文件改过会各占一条）。
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.idx)
}
