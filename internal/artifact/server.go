package artifact

import (
	"fmt"
	"html"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const csp = "default-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"script-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// Server 是只监听回环地址的本地预览服务。
// 懒启动：首次写入页面或用户显式打开预览时才 Listen。
type Server struct {
	addr   string
	root   string
	shared fs.FS

	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	baseURL string
	started bool
}

// NewServer 构造预览服务；root 是页面根目录。
func NewServer(addr, root string) *Server {
	return &Server{addr: addr, root: root, shared: SharedFS()}
}

// Ensure 启动服务（幂等）。只允许 127.0.0.1 / ::1 / localhost。
func (s *Server) Ensure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return fmt.Errorf("预览地址 %q 不合法: %w", s.addr, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("预览地址必须绑定回环地址（127.0.0.1 或 ::1），当前 %q", s.addr)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("启动预览服务失败: %w", err)
	}
	baseHost := ln.Addr().String()
	if h, p, err := net.SplitHostPort(baseHost); err == nil {
		baseHost = net.JoinHostPort(h, p)
	}
	s.ln = ln
	s.baseURL = "http://" + baseHost
	s.srv = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.started = true
	go func(srv *http.Server, ln net.Listener) {
		_ = srv.Serve(ln)
	}(s.srv, ln)
	return nil
}

// Close 停止预览服务。
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return nil
	}
	err := s.srv.Close()
	s.started = false
	s.srv = nil
	s.ln = nil
	s.baseURL = ""
	return err
}

// BaseURL 返回服务根地址；未启动时为空。
func (s *Server) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

// URL 返回页面的预览地址（总是指向 entry）。
func (s *Server) URL(slug, _ string) string {
	s.mu.Lock()
	base := s.baseURL
	s.mu.Unlock()
	if base == "" {
		return ""
	}
	return base + "/a/" + slug + "/"
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveIndex)
	mux.HandleFunc("/a/", s.serveArtifact)
	return mux
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	setSecurityHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pages, _ := ListPages(s.root)
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>本地页面预览</title><style>")
	b.WriteString(":root{color-scheme:light dark}body{font-family:system-ui,-apple-system,'Segoe UI',sans-serif;max-width:760px;margin:40px auto;padding:0 20px;line-height:1.6}")
	b.WriteString("h1{font-size:1.4rem}ul{list-style:none;padding:0}li{padding:12px 0;border-bottom:1px solid rgba(128,128,128,.25)}")
	b.WriteString("a{color:#2563eb;text-decoration:none;font-weight:600}a:hover{text-decoration:underline}")
	b.WriteString(".meta{color:#6b7280;font-size:.85rem;margin-left:8px}.warn{color:#b45309}")
	b.WriteString("</style><h1>本地页面预览</h1>")
	if len(pages) == 0 {
		b.WriteString("<p>还没有页面。让 agent 生成一个，或手动写入 <code>data/artifacts/&lt;slug&gt;/index.html</code>。</p>")
	} else {
		sort.SliceStable(pages, func(i, j int) bool { return pages[i].UpdatedAt.After(pages[j].UpdatedAt) })
		b.WriteString("<ul>")
		for _, p := range pages {
			link := "/a/" + url.PathEscape(p.Slug) + "/"
			status := ""
			if p.Status == "needs_review" {
				status = " <span class=\"warn\">needs review</span>"
			}
			fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a><span class=\"meta\">v%d · %s · %s</span>%s</li>",
				html.EscapeString(link), html.EscapeString(p.Title), p.Version, humanKB(p.Bytes),
				p.UpdatedAt.Format("01-02 15:04"), status)
		}
		b.WriteString("</ul>")
	}
	b.WriteString("<p class=\"meta\">设计系统组件：<a href=\"/a/_shared/gallery.html\">gallery.html</a></p></body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) serveArtifact(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/a/")
	parts := strings.SplitN(rest, "/", 2)
	slug := parts[0]
	file := ""
	if len(parts) == 2 {
		file = parts[1]
	}

	if slug == "_shared" {
		s.serveShared(w, r, file)
		return
	}
	if !slugPattern.MatchString(slug) {
		http.NotFound(w, r)
		return
	}
	// /a/<slug> 缺少尾斜杠时相对资源会解析到错误位置，统一跳到 /a/<slug>/。
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
		return
	}
	if file == "" {
		file = entryFor(s.root, slug)
	}
	clean := path.Clean(file)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		http.NotFound(w, r)
		return
	}
	target := filepath.Join(s.root, slug, filepath.FromSlash(clean))
	if !withinDir(filepath.Join(s.root, slug), target) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, target)
}

func (s *Server) serveShared(w http.ResponseWriter, r *http.Request, file string) {
	if file == "" || strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return
	}
	clean := path.Clean(file)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		http.NotFound(w, r)
		return
	}
	raw, err := fs.ReadFile(s.shared, clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(path.Ext(clean)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	_, _ = w.Write(raw)
}

func entryFor(root, slug string) string {
	m, err := readManifest(filepath.Join(root, slug))
	if err == nil && m.Entry != "" {
		return m.Entry
	}
	return "index.html"
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func humanKB(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
