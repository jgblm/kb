// Command kb 是个人代码知识库的命令行入口。
package main

import (
	"fmt"
	"os"

	"github.com/stone/kb/internal/cli"
)

// version 由 -ldflags "-X main.version=..." 注入。
var version = "0.1.0-dev"

func main() {
	if err := cli.Execute(version); err != nil {
		fmt.Fprintln(os.Stderr, "kb: "+err.Error())
		os.Exit(1)
	}
}
