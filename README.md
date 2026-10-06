# zig-cc-shim

A small shim that sits in front of `zig cc` / `zig c++` so that zig can serve as the hermetic C toolchain `cc` for kottera's native builds (kottera ADR-155, D3). It presents the command-line interface that Go's cgo, Rust's linker hook and node-gyp expect, and pins the target so that no host header or library leaks into a build. This repository is private until further notice; the implementation arrives through pull requests against `main`.

It is one static Go program (`CGO_ENABLED=0`). There is no shell script anywhere in this repository or its release: the shim, the staging command and the gate are all Go.

## The gate

Every change runs this one command before it merges, and CI runs the same command on every pull request and every push to `main`:

```
go run ./cmd/gate
```

It checks `gofmt -l .` and `go mod tidy -diff`, runs `go vet ./...` and a `CGO_ENABLED=0 go build -trimpath ./cmd/zig-cc-shim` for each of darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64, then `go build ./...` and `go test -count=1 ./...` with `CGO_ENABLED=0`. It needs no zig and no C compiler. It prints `gate: ok` when everything passes.

CI (`.github/workflows/gate.yml`) runs it on the repository's self-hosted runners, `[self-hosted, macOS, ARM64]` and `[self-hosted, Linux, X64]`. Hosted runners are never used: the organisation's hosted-runner billing is blocked, so a hosted job is never started.

## The integration tests

`cmd/stage/zig_test.go` runs the staged shim in front of a real zig. They need two environment variables:

| Variable | Names | Without it |
|---|---|---|
| `ZIG_CC_SHIM_TEST_ZIG` | the `zig` executable of an unpacked zig release, beside its `lib/` | every integration test skips, naming the variable |
| `ZIG_CC_SHIM_TEST_KOTTERA` | a `kottera/kottera` checkout | the darwin `go test -race ./pkg/toolchain` proof skips, naming the variable |

With `KOTTERA_TEST_BARE_HOST=1` (kottera ADR-151 D3), a missing variable fails the test instead of skipping it. CI sets none of these, so there the integration tests skip with their reason, and nothing in CI downloads zig. `CC_TOOLCHAIN_TARGET_GLIBC` and `CC_TOOLCHAIN_TARGET_MACOS` default to `2.31` and `13.0` when unset.

```
ZIG_CC_SHIM_TEST_ZIG=~/zig-0.17.0/zig ZIG_CC_SHIM_TEST_KOTTERA=~/src/kottera KOTTERA_TEST_BARE_HOST=1 \
  go test -count=1 -v -timeout 30m -run Zig ./cmd/stage
```

Each test stages the shim with `cmd/stage` into a temporary store whose `zig/` is a symlink to the release's directory, so nothing is written into the release. Then:

- `TestZigEndToEnd`: `cc` and `c++` compile, link and run a C and a C++ program; `ar` builds an archive that a program links; Go's two stdin probes (`-c - -o /dev/null` and `-### -x c -c -`) leave nothing in their working directory; `--tera-describe` reports `zig version`'s output and the host target.
- `TestZigKotteraRaceOnDarwin` (darwin only): a control arm runs `go test -race -count=1 ./pkg/toolchain` in the checkout with `CC` at bare `zig cc`, given the same target and SDK flags but no de-duplication, and asserts that it fails with dyld's `duplicate linked dylib` CoreFoundation abort. Then the same command with `CC` and `CXX` at the shim must pass, and must not leave a `-.o` in the checkout.
- `TestZigCgoRaceGlibcCeiling`: builds a cgo program with `-race` through the shim and asserts that the highest `GLIBC_` version it requires is at or below `CC_TOOLCHAIN_TARGET_GLIBC`. On Linux it builds natively and runs the program. Elsewhere it cross-builds for linux/amd64 with `x86_64-unknown-linux-gnu-cc` and only reads the ELF. It does not cross-build for linux/arm64: with zig 0.17.0 that `-race` link fails, because Go's linux/arm64 race runtime needs libgcc's outline atomics (`__aarch64_ldadd8_sync` and others), which zig does not supply.

## Roles

The shim chooses what to run from the basename of `argv[0]`:

| Invoked as | Runs |
|---|---|
| `cc`, `gcc`, `clang` | `zig cc -target <host target> …` |
| `c++`, `g++`, `clang++` | `zig c++ -target <host target> …` |
| `ar`, `ranlib` | `zig ar …`, `zig ranlib …` |
| `<triple>-cc`, `<triple>-c++` | `zig cc` / `zig c++` with the target fixed by the triple |

The triples are `x86_64-unknown-linux-gnu`, `aarch64-unknown-linux-gnu`, `aarch64-apple-darwin` and `x86_64-apple-darwin`, so Cargo's `CARGO_TARGET_<TRIPLE>_LINKER` can name the right one. Any other name is refused, and the refusal lists every role.

The shim replaces itself with zig through `execve` (`syscall.Exec`), so signals and the exit status are zig's.

## Where zig is

zig is `<resolved self>/../zig/zig`: the shim resolves its own executable path through any symlinks, then looks for `zig/zig` beside its `bin/`. That is the store layout of ADR-155 D7, `<store>/<os>-<arch>/<zig-version>/cc/{zig/{zig,lib/},bin/}`. No path is baked in or configured, so the store is relocatable. If zig is not there, the shim refuses and names the path it looked at.

## The target

The target comes only from the environment, which the `cc` toolchain definition contributes (ADR-155 D2, D3):

| Variable | Used for | Becomes |
|---|---|---|
| `CC_TOOLCHAIN_TARGET_GLIBC` | a Linux target | `-target <arch>-linux-gnu.<floor>`, e.g. `x86_64-linux-gnu.2.31` |
| `CC_TOOLCHAIN_TARGET_MACOS` | a darwin target | `-target <arch>-macos.<min>`, e.g. `aarch64-macos.13.0` |

A host role (`cc`, `c++` and their aliases) targets the host's own OS and architecture. A triple role targets its triple's, whatever the host. The version always comes from the variable.

**When the variable a role needs is unset, the shim refuses.** It exits 1 and names the variable and the repair. It never falls back to the host's own glibc or macOS version: that fallback is the defect ADR-155 D2 closes, because it makes two hosts build different things. A value that is not a dotted version number (for example `2.31 -I/usr/include`) is refused too. A role needs only the variable for its own target: a Linux `cc` does not need `CC_TOOLCHAIN_TARGET_MACOS`. `ar` and `ranlib` need neither.

Nothing else is added on Linux. The shim never passes a host search path (`/usr/include`, `/usr/lib`, `/usr/local/…`, `/opt/homebrew/…`), on any platform (ADR-155 D4).

## On darwin

The macOS SDK is the one host input. It is taken from, in order:

1. `SDKROOT`, if set. If it is set and is not a directory, the shim refuses rather than falling past it;
2. `/usr/bin/xcrun --sdk macosx --show-sdk-path`;
3. `/Library/Developer/CommandLineTools/SDKs/MacOSX.sdk`.

If none gives an SDK, the shim refuses and names `xcode-select --install` as the repair.

For a darwin target the shim adds ADR-155 D3 item 3's measured set, verbatim:

```
--sysroot <sdk> -idirafter <sdk>/usr/include -F/System/Library/Frameworks -L/usr/lib
```

zig turns `--sysroot` into the linker's `-syslibroot`, and resolves the absolute `-F` and `-L` against it. With zig 0.17.0 on darwin/arm64, `-v` and `ZIG_VERBOSE_LINK=1` showed `-F<sdk>/System/Library/Frameworks` to clang, and `-L<sdk>/usr/lib` and `-F <sdk>/System/Library/Frameworks` to the linker, with neither host directory passed. With an SDK that lacks those directories, zig warns that it cannot open the SDK path and drops it. It does not fall back to the host directory. So those two flags reach the SDK, not the host's `/usr/lib` or `/System/Library/Frameworks`.

Repeated `-framework X` and `-lX` (or `-l X`) arguments are de-duplicated, keeping the first. zig's Mach-O linker otherwise links the dylib twice, and dyld aborts with `duplicate linked dylib CoreFoundation`. This is for darwin targets only: on Linux a repeated static library can matter, so it is left alone.

## Compiles from stdin

`zig cc` ignores `-o` for stdin input and writes `./-.o`, and Go's compiler probes run `cc … -c - -o /dev/null`. When the input is `-` and the output is `/dev/null`, the shim gives zig a fresh output under `$TMPDIR` instead (`zig-cc-shim-stdin-*.o`). Go also identifies its compiler with `cc -### -x c -c -`, run in the package's directory. clang only prints the commands it would run, but `zig cc` runs them and writes `./-.o`. So a stdin compile under `-###` that names no output gets one under `$TMPDIR` too. Go reads only the version line of that output, so the compiler's identity does not change. The integration tests found this: a cgo `go test -race` of a kottera checkout through the shim left a `-.o` in the checkout.

The shim has already become zig by then, so it cannot remove that file. It is small, and it lives in the temporary directory.

## The glibc floor on Linux

On a Linux host building for a Linux target, the shim checks the host's glibc against `CC_TOOLCHAIN_TARGET_GLIBC` before it runs zig. It reads the version banner (`… release version 2.39`) from the dynamic loader at the path the architecture's ABI fixes (`/lib64/ld-linux-x86-64.so.2`, `/lib/ld-linux-aarch64.so.1`). If that file has no banner, it reads `libc.so.6` in the usual distro locations. These files are read, never passed to the compiler. Below the floor, or when no glibc can be found (a musl host), the shim refuses. The refusal names the host's glibc, where it was read, the floor and the repair.

## Refusals

Every refusal is ADR-109 D7's shape: what is missing, why that stops the shim, and the repair. It goes to stderr, the exit status is 1, and it says it is not transient:

```
zig-cc-shim (cc): refused: CC_TOOLCHAIN_TARGET_MACOS is not set, so there is no macOS minimum to target
  why: …
  repair: …
  This is not transient: retrying the same command will refuse again until the repair is made.
```

## `--tera-describe`

`<role> --tera-describe`, as the only argument, prints what the shim would use and exits 0. It does not run a compile. The format is stable: one `key: value` per line, in this order, with the optional lines omitted when they do not apply.

```
zig-cc-shim-describe: 1
shim: <module version, or (devel)>
shim-revision: <vcs revision the shim was built from, +modified if dirty, or unknown>
role: <the role invoked>
zig: <absolute path of the zig it runs>
zig-version: <`zig version`>
target: <zig -target, or none for ar and ranlib>
sdk: <darwin target only: the SDK path>
sdk-version: <darwin target only: SDKSettings.json's Version, or unknown>
host-glibc: <Linux host only: the host's glibc, or unknown>
```

`zig-cc-shim-describe` is the format's version. It changes only when a key is removed or a value's meaning changes. Adding a key does not change it, so a reader ignores keys it does not know. `--tera-describe` refuses where a compile would refuse for a missing target or SDK, but it does not apply the glibc floor check: it reports `host-glibc` instead.

## Not adopted

Uber's `hermetic_cc_toolchain` wrapper also rewrites `-u SYMBOL`, `-l:file` and C++'s default UBSan. None of those are adopted here. Each one is adopted only where a Wave 1 measurement hits it.

## Building and staging a shim beside zig

`cmd/stage` builds the shim from this checkout and lays out a store-shaped directory:

```
go run ./cmd/stage -dest <dir> [-goos linux -goarch amd64]
```

This writes `<dir>/bin/<role>` for every role. It does not write `<dir>/zig`, which is where an unpacked zig release goes. To stage a shim beside an official zig, as the Wave 1 measurements do, unpack the release into `<dir>/zig` without its versioned top-level directory, then stage:

```
mkdir -p ~/cc/zig
tar -xJf zig-aarch64-macos-0.17.0.tar.xz -C ~/cc/zig --strip-components=1   # gives ~/cc/zig/zig and ~/cc/zig/lib/
go run ./cmd/stage -dest ~/cc
export CC_TOOLCHAIN_TARGET_GLIBC=2.31 CC_TOOLCHAIN_TARGET_MACOS=13.0
~/cc/bin/cc --tera-describe
```

Then put `~/cc/bin` first on `PATH`, or point `CC`, `CXX`, `AR` or `CARGO_TARGET_<TRIPLE>_LINKER` at its entries.

**The role entries are hard links to one binary.** Copies would put sixteen identical binaries in the store and the release. Symlinks would break the shim under any build tool that resolves its compiler with `realpath` before running it: `argv[0]` would then be the link target's name, not the role. A hard link has the role's own name however it is resolved, and it costs one binary's space. kottera's `pkg/archive` supports hard links, so the release's tar can carry them as they are. A symlink that someone else points at a role entry still works: the role comes from the symlink's own name, and zig is found through the resolved path.
