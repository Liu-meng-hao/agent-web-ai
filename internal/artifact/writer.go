package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Options 是构造 Writer 的依赖。
type Options struct {
	Root      string
	MaxFileKB int
	SelfCheck bool
	Server    *Server
	OnWrite   func(WriteResult)
}

// WriteInput 对应 artifact_write 的入参。
type WriteInput struct {
	Slug    string `json:"slug" jsonschema:"description=页面标识，kebab-case，如 sales-weekly"`
	Title   string `json:"title,omitempty" jsonschema:"description=页面标题，写入 manifest 供预览列表展示"`
	Path    string `json:"path,omitempty" jsonschema:"description=页面内的相对路径，默认 index.html"`
	Content string `json:"content" jsonschema:"description=要写入的完整内容"`
	Mode    string `json:"mode,omitempty" jsonschema:"description=create|replace|patch，默认按页面是否存在自动选择"`
	Old     string `json:"old,omitempty" jsonschema:"description=仅 patch 模式：要被替换的原文片段，必须在文件中恰好出现一次"`
}

// WriteResult 是一次写入的结果，也用于 TUI/CLI 展示。
type WriteResult struct {
	Manifest
	Path         string
	WrittenBytes int64 // 本次写入文件的字节数（Manifest.Bytes 是入口页统计）
	URL          string
	Issues       []string
	Blocking     int    // 阻塞问题数；为 0 时 Issues 只是提示
	PreviewIn    string // 预览启动失败的原因；URL 为空时供工具回灌模型
}

// Writer 负责页面落盘、版本递增、自检与预览懒启动。
type Writer struct {
	root      string
	maxBytes  int64
	selfCheck bool
	server    *Server
	onWrite   func(WriteResult)

	mu sync.Mutex
}

// NewWriter 构造页面写入器。
func NewWriter(opts Options) *Writer {
	maxKB := opts.MaxFileKB
	if maxKB <= 0 {
		maxKB = 200
	}
	return &Writer{
		root:      opts.Root,
		maxBytes:  int64(maxKB) * 1024,
		selfCheck: opts.SelfCheck,
		server:    opts.Server,
		onWrite:   opts.OnWrite,
	}
}

// Root 返回页面根目录。
func (w *Writer) Root() string { return w.root }

// Write 写入页面并返回 manifest、预览 URL 与自检问题。
func (w *Writer) Write(ctx context.Context, in WriteInput) (WriteResult, error) {
	w.mu.Lock()
	res, err := w.writeLocked(in)
	w.mu.Unlock()
	if err != nil {
		return WriteResult{}, err
	}
	if w.onWrite != nil {
		w.onWrite(res)
	}
	return res, nil
}

func (w *Writer) writeLocked(in WriteInput) (WriteResult, error) {
	slug := strings.TrimSpace(in.Slug)
	if !slugPattern.MatchString(slug) {
		return WriteResult{}, fmt.Errorf("slug %q 不合法：只能用小写字母、数字和连字符，且以字母或数字开头（最长 64）", slug)
	}
	rel, err := cleanRelPath(in.Path)
	if err != nil {
		return WriteResult{}, err
	}
	if !utf8.ValidString(in.Content) {
		return WriteResult{}, errors.New("content 不是合法的 UTF-8 文本")
	}

	dir := filepath.Join(w.root, slug)
	filePath := filepath.Join(dir, filepath.FromSlash(rel))
	if !withinDir(dir, filePath) {
		return WriteResult{}, fmt.Errorf("path %q 越出页面目录", in.Path)
	}

	old, err := readManifest(dir)
	if err != nil && !os.IsNotExist(err) {
		return WriteResult{}, err
	}

	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	switch mode {
	case "":
		if old != nil {
			mode = "replace"
		} else {
			mode = "create"
		}
	case "create", "replace", "patch":
	default:
		return WriteResult{}, fmt.Errorf("mode 只能是 create/replace/patch，当前 %q", in.Mode)
	}

	content := in.Content
	switch mode {
	case "create":
		if old != nil {
			return WriteResult{}, fmt.Errorf("页面 %q 已存在（v%d），如需修改请用 replace 或 patch", slug, old.Version)
		}
		if _, err := os.Stat(filePath); err == nil {
			return WriteResult{}, fmt.Errorf("%s 已存在，如需覆盖请用 replace", rel)
		}
	case "replace":
		// 允许创建缺失的页面文件。
	case "patch":
		if old == nil {
			return WriteResult{}, fmt.Errorf("页面 %q 不存在，不能 patch", slug)
		}
		if in.Old == "" {
			return WriteResult{}, errors.New("patch 模式必须提供 old（被替换的原文片段）")
		}
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return WriteResult{}, fmt.Errorf("读取待修改文件失败: %w", err)
		}
		existing := string(raw)
		switch strings.Count(existing, in.Old) {
		case 0:
			return WriteResult{}, errors.New("old 片段在文件中不存在，patch 未执行；请先 fs_read 确认原文")
		case 1:
		default:
			return WriteResult{}, errors.New("old 片段在文件中出现多次，请提供更长的唯一片段")
		}
		content = strings.Replace(existing, in.Old, in.Content, 1)
	}

	if strings.TrimSpace(content) == "" {
		return WriteResult{}, errors.New("写入后文件为空")
	}
	if !utf8.ValidString(content) {
		return WriteResult{}, errors.New("写入后内容不是合法的 UTF-8 文本")
	}
	if int64(len(content)) > w.maxBytes {
		return WriteResult{}, fmt.Errorf("文件 %d 字节，超过上限 %d 字节（config artifact.max_file_kb）", len(content), w.maxBytes)
	}

	// 先落盘再自检：页面引用稍后写入的 CSS/图片时，自检只会给出提示，
	// 不会因为文件尚未创建而直接判失败。
	if err := atomicWrite(filePath, []byte(content), 0o644); err != nil {
		return WriteResult{}, fmt.Errorf("写入 %s 失败: %w", rel, err)
	}

	checked := w.selfCheck && isHTML(rel)
	var details []Issue
	blocking := 0
	if checked {
		details = CheckDetailed(content, dir)
		for _, d := range details {
			if d.Blocking {
				blocking++
			}
		}
	}

	now := time.Now()
	m := Manifest{
		Slug:      slug,
		Title:     strings.TrimSpace(in.Title),
		Entry:     rel,
		CreatedAt: now,
		UpdatedAt: now,
		Bytes:     int64(len(content)),
		SHA256:    sha256Hex(content),
		Status:    "ok",
	}
	if old != nil {
		m.Version = old.Version
		m.CreatedAt = old.CreatedAt
		m.ReviewFailures = old.ReviewFailures
		m.Status = old.Status
		if m.Title == "" {
			m.Title = old.Title
		}
		if old.Entry != "" {
			m.Entry = old.Entry
			if isHTML(rel) && !isHTML(old.Entry) {
				m.Entry = rel
			}
		}
	}
	if m.Title == "" {
		m.Title = slug
	}
	// 统计只描述入口页，避免写 style.css 时覆盖入口页的体积与 sha256。
	if old != nil && m.Entry != rel {
		m.Bytes = old.Bytes
		m.SHA256 = old.SHA256
	}
	m.Version++

	if checked {
		if blocking > 0 {
			m.ReviewFailures++
			if m.ReviewFailures >= 2 {
				m.Status = "needs_review"
			}
		} else {
			m.ReviewFailures = 0
			m.Status = "ok"
		}
	} else if m.Status == "" {
		m.Status = "ok"
	}

	if err := writeManifest(dir, &m); err != nil {
		return WriteResult{}, fmt.Errorf("写入 manifest 失败: %w", err)
	}

	issues := make([]string, 0, len(details))
	for _, d := range details {
		issues = append(issues, d.Text)
	}
	res := WriteResult{
		Manifest:     m,
		Path:         rel,
		WrittenBytes: int64(len(content)),
		Issues:       issues,
		Blocking:     blocking,
	}
	if w.server != nil {
		if err := w.server.Ensure(); err != nil {
			res.PreviewIn = err.Error()
		} else {
			res.URL = w.server.URL(slug, m.Entry)
		}
	}
	return res, nil
}

// ListPages 读取所有页面的 manifest，按更新时间倒序。
func ListPages(root string) ([]Manifest, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := readManifest(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func cleanRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "index.html"
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "", errors.New("path 必须是页面内的相对路径，不能以 / 开头")
	}
	if strings.Contains(p, ":") {
		return "", errors.New("path 不能包含冒号或盘符")
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q 越出页面目录", p)
	}
	if clean == manifestName {
		return "", errors.New("path 不能是 manifest.json")
	}
	return clean, nil
}

func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func isHTML(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".html", ".htm":
		return true
	}
	return false
}
