package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Jail 把文件与命令的落脚点限制在一组根目录内。
//
// 判定流程：绝对化 → 解析符号链接（叶子不存在时解析最近的已存在祖先）→
// 用 filepath.Rel 判断是否越界。Windows 上额外做大小写归一。
type Jail struct {
	roots []string
}

// NewJail 构造 jail；根目录会先绝对化并解析符号链接。
func NewJail(paths ...string) (*Jail, error) {
	if len(paths) == 0 {
		return nil, errors.New("jail: 至少需要一个根目录")
	}
	roots := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("jail: 解析根目录 %s 失败: %w", p, err)
		}
		roots = append(roots, resolvePath(abs))
	}
	if len(roots) == 0 {
		return nil, errors.New("jail: 根目录全部为空")
	}
	return &Jail{roots: roots}, nil
}

// Roots 返回归一化后的根目录（用于诊断输出）。
func (j *Jail) Roots() []string { return append([]string(nil), j.roots...) }

// Check 校验路径是否在 jail 内，返回解析后的绝对路径。
func (j *Jail) Check(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("路径为空")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("解析路径 %s 失败: %w", p, err)
	}
	real := resolvePath(abs)
	for _, root := range j.roots {
		if within(root, real) {
			return real, nil
		}
	}
	return "", fmt.Errorf("路径越界：%s 不在允许目录内（%s）", p, strings.Join(j.roots, ", "))
}

// CheckDir 在 Check 基础上要求目标存在且是目录。
func (j *Jail) CheckDir(p string) (string, error) {
	real, err := j.Check(p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("目录不存在：%s", real)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("不是目录：%s", real)
	}
	return real, nil
}

func within(root, p string) bool {
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		p = strings.ToLower(p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePath 解析符号链接；路径不存在时，解析最近的已存在祖先并拼回剩余部分。
func resolvePath(p string) string {
	var missing []string
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				real = filepath.Join(real, missing[i])
			}
			return real
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}
