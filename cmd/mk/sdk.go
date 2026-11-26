// Why: the system views mk carries and the system headers a C import reads
// are facts of this mk and this machine; a user needs to see them.
//
// What: runSDK, the `mk sdk [views]` command.
//
// How: driver.SDKText writes the text; `views` limits it to the views.
// Exit 0, or 2 for a usage error.

package main

import (
	"flag"
	"fmt"
	"io"

	"mk/internal/driver"
)

func init() {
	register(command{"sdk", "sdk     [views]                the system views mk carries and the system headers C imports read", runSDK})
}

func runSDK(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mk sdk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	switch {
	case len(rest) == 0:
		fmt.Fprint(stdout, driver.SDKText(false))
	case len(rest) == 1 && rest[0] == "views":
		fmt.Fprint(stdout, driver.SDKText(true))
	default:
		fmt.Fprintln(stderr, "usage: mk sdk [views]")
		return 2
	}
	return 0
}
