// camera-drive is one static binary; each subcommand replaces a script.
package main

import (
	"fmt"
	"os"
)

// commands maps a subcommand name to its entry point. web plugs in here.
var commands = map[string]func(args []string) int{
	"offload": cmdOffload,
	"wipe":    cmdWipe,
	"live":    cmdLive,
}

func main() {
	if len(os.Args) >= 2 {
		if cmd, ok := commands[os.Args[1]]; ok {
			os.Exit(cmd(os.Args[2:]))
		}
	}
	fmt.Fprintln(os.Stderr, "usage: camera-drive offload|wipe|live [args]")
	os.Exit(2)
}
