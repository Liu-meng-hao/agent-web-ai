package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent/internal/memory"
)

func TestOpenMemoryFallsBackToReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	writer, err := memory.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, _, err := writer.Write(memory.NewFact("已存在的事实")); err != nil {
		t.Fatal(err)
	}

	ro, readOnly, err := openMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if !readOnly || !ro.ReadOnly() {
		t.Fatalf("应降级为只读: readOnly=%v store.ReadOnly=%v", readOnly, ro.ReadOnly())
	}
	if facts := ro.Active(""); len(facts) != 1 {
		t.Fatalf("只读视图应能读到已有事实: %+v", facts)
	}
	if _, _, err := ro.Write(memory.NewFact("新事实")); err == nil || !strings.Contains(err.Error(), "只读") {
		t.Fatalf("只读视图不应允许写入: %v", err)
	}
}

func TestOpenMemoryNoFallbackOnOtherErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, readOnly, err := openMemory(file)
	if err == nil || s != nil || readOnly {
		t.Fatalf("非锁冲突不应降级: store=%v readOnly=%v err=%v", s, readOnly, err)
	}
}
