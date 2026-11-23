// Command agent 是终端 agent 的唯一入口。
package main

import (
	"errors"
	"fmt"
	"os"

	"agent/internal/cli"
)

func main() {
	err := cli.Execute()
	if err == nil {
		return
	}
	// doctor 已经在输出里列出了失败项与汇总，main 不再重复打印。
	if errors.Is(err, cli.ErrDoctorFailed) {
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "错误："+err.Error())
	os.Exit(1)
}
