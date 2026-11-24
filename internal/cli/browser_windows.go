//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

// shortPath 把含空格的 Windows 长路径转成 8.3 短路径。
//
// 参数经 npx → cmd 转发时按空格切分，带空格的长路径会被拆成两段，
// 表现为 "'D:\Program' is not recognized"。短路径没有空格，可安全传递。
func shortPath(p string) string {
	if p == "" {
		return p
	}
	src, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, syscall.MAX_PATH)
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW").Call(
		uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if r == 0 || r >= uintptr(len(buf)) {
		return p
	}
	return syscall.UTF16ToString(buf)
}
