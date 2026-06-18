package main

import (
	"fmt"
	"os"

	"github.com/sova-lang/sova-fonts/internal/cmds"
)

func main() {
	if err := cmds.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
