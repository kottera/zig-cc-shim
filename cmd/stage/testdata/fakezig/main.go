// Command fakezig stands in for zig in the staging test: it prints the
// arguments it was given as JSON, answers `version`, and exits with
// FAKEZIG_EXIT when that is set.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println("0.0.0-fake")
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(os.Args[1:])
	if code, err := strconv.Atoi(os.Getenv("FAKEZIG_EXIT")); err == nil {
		os.Exit(code)
	}
}
