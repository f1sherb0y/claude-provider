// ccp: quickly switch the API provider of Claude Code / Codex.
package main

import "os"

func main() {
	os.Exit(cliMain(os.Args[1:]))
}
