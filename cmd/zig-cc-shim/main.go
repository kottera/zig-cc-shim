// Command zig-cc-shim is the C toolchain's `cc`, `c++`, `ar` and their
// siblings in front of zig. It is installed once per role name in the store's
// bin/, and dispatches on the name it was invoked as. See the README.
package main

import (
	"os"

	"github.com/kottera/zig-cc-shim/internal/shim"
)

func main() {
	os.Exit(shim.Main(os.Args, os.Stdout, os.Stderr))
}
