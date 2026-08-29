// Command pgdsn-validate validates a PostgreSQL DSN supplied through a named
// environment variable. It intentionally emits only pass/fail, never the DSN.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
)

var lookupEnv = os.LookupEnv

func main() {
	name := flag.String("dsn-env", "", "environment variable containing the DSN")
	requireLoopback := flag.Bool("require-loopback", false, "require a loopback host")
	flag.Parse()
	if *name == "" {
		fmt.Fprintln(os.Stderr, "--dsn-env is required")
		os.Exit(2)
	}
	raw, ok := lookupEnv(*name)
	if !ok || raw == "" {
		fmt.Fprintln(os.Stderr, "DSN environment variable is empty")
		os.Exit(2)
	}
	if err := pgdsn.Validate(raw, false); err != nil {
		fmt.Fprintln(os.Stderr, "DSN rejected")
		os.Exit(1)
	}
	if *requireLoopback {
		if err := pgdsn.RequireLoopback(raw); err != nil {
			fmt.Fprintln(os.Stderr, "DSN host is not loopback")
			os.Exit(1)
		}
	}
	fmt.Println("ok")
}
