//go:build !windows

package cli

// shortPath 在非 Windows 平台不需要处理空格转义，原样返回。
func shortPath(p string) string { return p }
