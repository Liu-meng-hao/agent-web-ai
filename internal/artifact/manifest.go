package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const manifestName = "manifest.json"

// Manifest 是单个页面的元数据，也是版本与预览入口的事实源。
type Manifest struct {
	Version        int       `json:"version"`
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	Entry          string    `json:"entry"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Bytes          int64     `json:"bytes"`
	SHA256         string    `json:"sha256"`
	Status         string    `json:"status"`
	ReviewFailures int       `json:"review_failures,omitempty"`
}

func manifestPath(dir string) string { return filepath.Join(dir, manifestName) }

func readManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(manifestPath(dir))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", manifestName, err)
	}
	return &m, nil
}

func writeManifest(dir string, m *Manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicWrite(manifestPath(dir), raw, 0o644)
}

func atomicWrite(path string, raw []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
