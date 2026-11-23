package artifact

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodPage = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">` +
	`<meta name="viewport" content="width=device-width,initial-scale=1"><title>测试</title></head>` +
	`<body><main><h1>测试</h1><p>ok</p></main></body></html>`

func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	root := t.TempDir()
	return NewWriter(Options{Root: root, MaxFileKB: 200, SelfCheck: true}), root
}

func TestWriterCreateAndVersion(t *testing.T) {
	w, root := newTestWriter(t)
	ctx := context.Background()

	res, err := w.Write(ctx, WriteInput{Slug: "weekly", Title: "周报", Path: "index.html", Content: goodPage, Mode: "create"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Version != 1 || res.Status != "ok" || len(res.Issues) != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Entry != "index.html" || res.Bytes == 0 || res.SHA256 == "" {
		t.Fatalf("manifest 字段缺失: %+v", res.Manifest)
	}
	if _, err := os.Stat(filepath.Join(root, "weekly", "index.html")); err != nil {
		t.Fatalf("页面未落盘: %v", err)
	}

	res, err = w.Write(ctx, WriteInput{Slug: "weekly", Content: goodPage, Mode: "replace"})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if res.Version != 2 {
		t.Fatalf("版本应递增到 2，得到 %d", res.Version)
	}
	if res.Title != "周报" {
		t.Fatalf("replace 未保留标题: %q", res.Title)
	}

	if _, err := w.Write(ctx, WriteInput{Slug: "weekly", Content: goodPage, Mode: "create"}); err == nil {
		t.Fatal("create 已存在页面应失败")
	}
}

func TestWriterPatch(t *testing.T) {
	w, _ := newTestWriter(t)
	ctx := context.Background()
	if _, err := w.Write(ctx, WriteInput{Slug: "p", Content: goodPage}); err != nil {
		t.Fatal(err)
	}

	res, err := w.Write(ctx, WriteInput{Slug: "p", Mode: "patch", Old: "<p>ok</p>", Content: "<p>done</p>"})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if res.Version != 2 {
		t.Fatalf("版本 = %d", res.Version)
	}
	raw, err := os.ReadFile(filepath.Join(w.Root(), "p", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<p>done</p>") || strings.Contains(string(raw), "<p>ok</p>") {
		t.Fatalf("patch 未生效: %s", raw)
	}

	if _, err := w.Write(ctx, WriteInput{Slug: "p", Mode: "patch", Old: "不存在", Content: "x"}); err == nil {
		t.Fatal("old 不存在时应失败")
	}
	if _, err := w.Write(ctx, WriteInput{Slug: "p", Mode: "patch", Old: "html", Content: "x"}); err == nil {
		t.Fatal("old 多次出现时应失败")
	}
}

func TestWriterRejectsBadInput(t *testing.T) {
	w, _ := newTestWriter(t)
	ctx := context.Background()

	cases := []WriteInput{
		{Slug: "Bad Slug", Content: goodPage},
		{Slug: "ok", Path: "../escape.html", Content: goodPage},
		{Slug: "ok", Path: "/abs.html", Content: goodPage},
		{Slug: "ok", Path: "manifest.json", Content: goodPage},
		{Slug: "ok", Content: "   "},
		{Slug: "ok", Content: goodPage, Mode: "append"},
	}
	for _, in := range cases {
		if _, err := w.Write(ctx, in); err == nil {
			t.Fatalf("应拒绝非法输入: %+v", in)
		}
	}
}

func TestWriterSelfCheckAndNeedsReview(t *testing.T) {
	w, _ := newTestWriter(t)
	ctx := context.Background()
	bad := `<!doctype html><html><head><link href="https://cdn.example.com/x.css" rel="stylesheet"></head>` +
		`<body><div><span>x</div><img src="missing.png"><p>TODO</p></body></html>`

	res, err := w.Write(ctx, WriteInput{Slug: "bad", Content: bad})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) < 3 {
		t.Fatalf("期望标签/外链/缺失资源/占位问题，得到 %v", res.Issues)
	}
	if res.Status == "needs_review" {
		t.Fatal("首次失败不应直接 needs_review")
	}

	res, err = w.Write(ctx, WriteInput{Slug: "bad", Content: bad})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "needs_review" || res.ReviewFailures < 2 {
		t.Fatalf("连续失败应标记 needs_review: %+v", res.Manifest)
	}

	res, err = w.Write(ctx, WriteInput{Slug: "bad", Content: goodPage})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || res.ReviewFailures != 0 {
		t.Fatalf("通过后应恢复 ok: %+v", res.Manifest)
	}
}

func TestWriterWithoutPreview(t *testing.T) {
	w, _ := newTestWriter(t)
	res, err := w.Write(context.Background(), WriteInput{Slug: "np", Content: goodPage})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "" || res.PreviewIn != "" {
		t.Fatalf("未配置服务时不应有预览信息: %+v", res)
	}
}

func TestServerServesPagesAndHeaders(t *testing.T) {
	root := t.TempDir()
	srv := NewServer("127.0.0.1:0", root)
	w := NewWriter(Options{Root: root, MaxFileKB: 200, SelfCheck: true, Server: srv})
	res, err := w.Write(context.Background(), WriteInput{Slug: "demo", Title: "演示", Content: goodPage})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if res.URL == "" {
		t.Fatal("写入后应拿到预览 URL")
	}

	resp, err := http.Get(res.URL)
	if err != nil {
		t.Fatalf("GET 页面: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "测试") {
		t.Fatalf("页面内容异常: %d %s", resp.StatusCode, body)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if resp.Header.Get(h) == "" {
			t.Fatalf("缺少安全响应头 %s", h)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'none'") {
		t.Fatalf("CSP 缺少 form-action: %s", csp)
	}

	shared, err := http.Get(srv.BaseURL() + "/a/_shared/gallery.html")
	if err != nil {
		t.Fatal(err)
	}
	sharedBody, _ := io.ReadAll(shared.Body)
	shared.Body.Close()
	if shared.StatusCode != 200 || !strings.Contains(string(sharedBody), "<html") {
		t.Fatalf("shared 资源异常: %d", shared.StatusCode)
	}

	index, err := http.Get(srv.BaseURL() + "/")
	if err != nil {
		t.Fatal(err)
	}
	indexBody, _ := io.ReadAll(index.Body)
	index.Body.Close()
	if index.StatusCode != 200 || !strings.Contains(string(indexBody), "演示") {
		t.Fatalf("索引页异常: %d", index.StatusCode)
	}

	missing, err := http.Get(srv.BaseURL() + "/a/demo/nope.html")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("缺失文件应 404，得到 %d", missing.StatusCode)
	}
}

func TestServerRejectsNonLoopback(t *testing.T) {
	srv := NewServer("0.0.0.0:0", t.TempDir())
	if err := srv.Ensure(); err == nil {
		srv.Close()
		t.Fatal("绑定 0.0.0.0 应被拒绝")
	}
	if err := NewServer(":0", t.TempDir()).Ensure(); err == nil {
		t.Fatal("空 host 应被拒绝")
	}
}

func TestMirrorAssets(t *testing.T) {
	dest := t.TempDir()
	if err := MirrorAssets(dest); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dest, "tokens.css")
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("tokens.css 未镜像: %v", err)
	}
	custom := []byte("/* customized */")
	if err := os.WriteFile(target, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MirrorAssets(dest); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(target)
	if string(raw) != string(custom) {
		t.Fatal("MirrorAssets 不应覆盖已有文件")
	}
}

func TestCheckCleanPage(t *testing.T) {
	if issues := Check(goodPage, t.TempDir()); len(issues) != 0 {
		t.Fatalf("合格页面不应有问题: %v", issues)
	}
	withURL := strings.Replace(goodPage, "<p>ok</p>", "<p>来源：https://example.org/report</p>", 1)
	if issues := Check(withURL, t.TempDir()); len(issues) != 0 {
		t.Fatalf("正文里的纯文本 URL 不应算外链资源: %v", issues)
	}
}

func TestCheckOptionalEndTags(t *testing.T) {
	pages := []string{
		`<!doctype html><html lang="zh-CN"><body><ul><li>a<li>b</ul></body></html>`,
		`<!doctype html><html lang="zh-CN"><body><table><tr><td>a<td>b</tr></table></body></html>`,
		`<!doctype html><html lang="zh-CN"><body><p>a<div>b</div></body></html>`,
		`<!doctype html><html lang="zh-CN"><body><dl><dt>a<dd>b</dl></body></html>`,
	}
	for _, page := range pages {
		if issues := Check(page, t.TempDir()); len(issues) != 0 {
			t.Fatalf("可选闭合标签被误报: %v\n%s", issues, page)
		}
	}

	bad := `<!doctype html><html lang="zh-CN"><body><div><span>x</div></body></html>`
	if issues := Check(bad, t.TempDir()); len(issues) == 0 {
		t.Fatal("真实嵌套错误应被检出")
	}
}

func TestCheckAdvisoriesAreNonBlocking(t *testing.T) {
	page := `<!doctype html><html lang="zh-CN"><body><main><p>TODO</p>` +
		`<img src="later.png"><a href="https://example.org/source">来源</a></main></body></html>`
	issues := CheckDetailed(page, t.TempDir())
	if len(issues) == 0 {
		t.Fatal("应给出提示")
	}
	for _, issue := range issues {
		if issue.Blocking {
			t.Fatalf("这些情况不应阻塞: %+v", issue)
		}
	}

	w, _ := newTestWriter(t)
	for i := 0; i < 2; i++ {
		res, err := w.Write(context.Background(), WriteInput{Slug: "advisory", Content: page})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "ok" || res.ReviewFailures != 0 || res.Blocking != 0 {
			t.Fatalf("仅提示不应标记 needs_review: %+v", res.Manifest)
		}
	}
}

func TestCheckSharedReferenceIsBlocking(t *testing.T) {
	page := strings.Replace(goodPage, "</head>", `<link rel="stylesheet" href="/a/_shared/base.css"></head>`, 1)
	issues := CheckDetailed(page, t.TempDir())
	blocking := 0
	for _, issue := range issues {
		if issue.Blocking {
			blocking++
		}
	}
	if blocking == 0 {
		t.Fatalf("引用 _shared 应提示脱离预览会失效: %v", issues)
	}
}

func TestManifestStatsFollowEntry(t *testing.T) {
	w, _ := newTestWriter(t)
	ctx := context.Background()
	res, err := w.Write(ctx, WriteInput{Slug: "stats", Path: "index.html", Content: goodPage})
	if err != nil {
		t.Fatal(err)
	}
	entryBytes, entrySHA := res.Bytes, res.SHA256

	res, err = w.Write(ctx, WriteInput{Slug: "stats", Path: "style.css", Content: "body{color:#111}", Mode: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != entryBytes || res.SHA256 != entrySHA {
		t.Fatalf("写 CSS 不应覆盖入口页统计: %+v", res.Manifest)
	}
	if res.WrittenBytes != int64(len("body{color:#111}")) {
		t.Fatalf("WrittenBytes = %d", res.WrittenBytes)
	}
	if res.Entry != "index.html" {
		t.Fatalf("entry = %q", res.Entry)
	}

	res, err = w.Write(ctx, WriteInput{Slug: "stats", Path: "index.html", Content: strings.Replace(goodPage, "<p>ok</p>", "<p>changed</p>", 1), Mode: "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA256 == entrySHA {
		t.Fatal("重写入口页后 sha256 应变化")
	}
}
