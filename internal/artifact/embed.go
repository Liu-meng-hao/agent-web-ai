package artifact

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed assets/design-system/*
var embedded embed.FS

// SharedFS 返回内嵌的设计系统文件，供预览服务 /a/_shared/ 使用。
func SharedFS() fs.FS {
	sub, err := fs.Sub(embedded, "assets/design-system")
	if err != nil {
		return embedded
	}
	return sub
}

// MirrorAssets 把设计系统镜像到磁盘，供模型用 fs_read 读取并内联到成品页。
// 已存在的文件不覆盖，避免抹掉用户本地修改。
func MirrorAssets(dest string) error {
	root := SharedFS()
	return fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return nil
		}
		raw, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}
