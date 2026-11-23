package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJailAllowsInside(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	j, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		root,
		sub,
		filepath.Join(sub, "not-exist-yet.txt"),
		filepath.Join(root, "a", "..", "b"),
	} {
		if _, err := j.Check(p); err != nil {
			t.Errorf("Check(%q) 应通过，却报错: %v", p, err)
		}
	}
}

func TestJailRejectsEscape(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)
	jail, err := NewJail(filepath.Join(root, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "work"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		parent,
		filepath.Join(root, "other"),
		filepath.Join(root, "work", "..", "..", "x"),
	}
	for _, p := range cases {
		if _, err := jail.Check(p); err == nil {
			t.Errorf("Check(%q) 应被拒绝", p)
		}
	}
}

func TestJailRejectsSiblingPrefix(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "data2"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 前缀相同但不同目录，不能因为字符串前缀而放行。
	if _, err := jail.Check(filepath.Join(root, "data2", "x")); err == nil {
		t.Fatalf("data2 是 data 的同级目录，应被拒绝")
	}
}

func TestJailCheckDir(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := jail.CheckDir(file); err == nil {
		t.Fatalf("文件不应通过 CheckDir")
	}
	if _, err := jail.CheckDir(root); err != nil {
		t.Fatalf("目录应通过 CheckDir: %v", err)
	}
}

func TestJailRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前环境无法创建符号链接，跳过: %v", err)
	}
	if _, err := jail.Check(filepath.Join(link, "secret.txt")); err == nil {
		t.Fatalf("符号链接指向 jail 外，应被拒绝")
	}
}
