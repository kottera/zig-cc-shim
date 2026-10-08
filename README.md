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
- `TestZigDevNullOutputsBounded`: several compiles to `/dev/null` with `TMPDIR` at an empty directory leave one output each; once those are aged past the grace period, the next compile leaves only its own, which zig wrote. The working directory holds only the source.
- `TestZigCallerSearchDirsOnDarwin` (darwin only): builds a static library and a framework in a temporary directory, outside the SDK, and links a program from another directory against each through an absolute `-L` or `-F`, joined and separate, and through a `-L` in an `@file` response file, then runs it. Control arms first assert that bare `zig cc` with the shim's target and SDK flags does not find the library, given the `-L` directly or in the `@file`.
- `TestZigBundleOnDarwin` (darwin only): a control arm asserts that bare `zig c++`, given the shim's target and SDK flags and node-gyp's macOS module link (`-bundle -undefined dynamic_lookup …`), does not link. Then that link through the shim must produce a Mach-O dylib, which a host program `dlopen()`s and which resolves an undefined symbol against that host. When `node` is on `PATH` with its `include/node/node_api.h`, an N-API addon linked the same way must `require()` and run in node. Otherwise that arm skips, naming why. `TestZigBundleLoaderRefused` (darwin only) asserts that `-bundle -bundle_loader <exe>` is refused.
- `TestZigLinuxLinkLineSearchDirs`: links a C program with `-lm` for Linux (natively on Linux, with `x86_64-unknown-linux-gnu-cc` elsewhere) under `ZIG_VERBOSE_LINK=1`. It asserts that the only `-L` on zig's final ld.lld line is zig's own `-L/`, and that no `-l` reaches ld.lld. It logs whether `-L/` is there.
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

zig's own Linux link line still carries `-L/`. zig 0.17.0 passes ld.lld `--as-needed -L/` just before its glibc stubs on every dynamic ELF link: an executable, a `-shared` library, its own stub `libc.so.6` and the rest, and a musl target too. `-L/` is a string literal in zig's ELF linker driver, beside `--build-id=none`, `-znow`, `-znorelro` and `libc_nonshared.a`. The shim does not name it, and no `zig cc` argument removes it without changing what zig links: only `-static`, `-nostdlib` and `-nolibc` do. So the shim leaves it. It is inert. zig resolves every `-l` itself, against the caller's `-L` directories only (a missing one fails with `searched paths: none`, never `/`), and hands ld.lld each library by absolute path. zig refuses `-Wl,-l…` and `-Xlinker -l…`, so no `-l` reaches ld.lld for it to search `/` with. `TestZigLinuxLinkLineSearchDirs` pins both halves: the only `-L` on zig's final link line is its own `-L/`, and no `-l` reaches ld.lld.

## On darwin

The macOS SDK is the one host input. The shim finds it by reading the filesystem only, and runs no `xcrun`: under a sandbox that grants the SDKs directory and nothing else, `xcrun` cannot run (kottera's W1-A4 measured this on a host whose only SDK was inside Xcode.app; PLAN-2026-10 R1). It is taken from, in order:

1. `SDKROOT`, if set. If it is set and is not a readable directory, the shim refuses rather than falling past it;
2. `DEVELOPER_DIR`, if set: the SDK inside the developer directory it selects, `<dir>/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk` for an Xcode or `<dir>/SDKs/MacOSX.sdk` for the Command Line Tools. An `Xcode.app` itself is accepted too, as `xcode-select` accepts it. If that directory has no SDK, the shim refuses. It never uses an SDK from a different developer directory than the one `DEVELOPER_DIR` selects;
3. `/Library/Developer/CommandLineTools/SDKs/MacOSX.sdk`;
4. `/Applications/Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk`.

When both the Command Line Tools and Xcode.app are installed and neither variable is set, the Command Line Tools' SDK wins. That is the SDK a sandboxed build already got before this order existed (`xcrun` failed and the shim fell back to it), so no host that worked changes SDK. Xcode.app is the place to look on a host that has nothing else. An Xcode installed anywhere else is selected with `DEVELOPER_DIR`.

In steps 3 and 4 the first SDK that is present decides. If it is present but cannot be read (a sandbox's `EPERM`), the shim refuses rather than taking the next one, so a sandbox's grants never change which SDK a host builds with.

Each refusal names the repair for that host: grant read access to the SDK that is present, fix or unset `SDKROOT` (naming the SDK the shim would otherwise take), or point `DEVELOPER_DIR` at a developer directory with an SDK. `xcode-select --install` is named only when the host has no SDK at all.

For a darwin target the shim adds ADR-155 D3 item 3's measured set, verbatim:

```
--sysroot <sdk> -idirafter <sdk>/usr/include -F/System/Library/Frameworks -L/usr/lib
```

zig turns `--sysroot` into the linker's `-syslibroot`, and resolves the absolute `-F` and `-L` against it. With zig 0.17.0 on darwin/arm64, `-v` and `ZIG_VERBOSE_LINK=1` showed `-F<sdk>/System/Library/Frameworks` to clang, and `-L<sdk>/usr/lib` and `-F <sdk>/System/Library/Frameworks` to the linker, with neither host directory passed. With an SDK that lacks those directories, zig warns that it cannot open the SDK path and drops it. It does not fall back to the host directory. So those two flags reach the SDK, not the host's `/usr/lib` or `/System/Library/Frameworks`.

zig re-roots a caller's absolute `-L` and `-F` the same way, and drops the result when the SDK has no such directory, so `#cgo LDFLAGS: -L${SRCDIR}/lib`, a Homebrew prefix or a Rust build script's `OUT_DIR` would never be searched (kottera W1-A4). zig leaves a relative directory alone and opens it from the working directory. So when a caller's `-L<dir>`, `-L <dir>`, `-F<dir>` or `-F <dir>` names an absolute directory that exists, lies outside the SDK, and has no counterpart `<sdk><dir>`, the shim respells it as that directory relative to the working directory, with symlinks resolved on both sides, keeping the caller's form (kottera PLAN-2026-10-hermetic-c-toolchain, ruling R2). `-L/opt/homebrew/lib` from `/Users/me/src/pkg` becomes `-L../../../../opt/homebrew/lib`. The new path comes only from the caller's argument, never from a path the shim names. Everything else is passed through as it was: a relative directory, a directory inside the SDK, one the SDK also has (a caller's `-L/usr/lib` still means `<sdk>/usr/lib`), and an absolute directory that does not exist, which zig re-roots under the SDK and, finding nothing there, drops with a warning, as before. The measured flag set above is unchanged.

A build system that passes its arguments in an `@file` response file passes its `-L` and `-F` there too, and zig re-roots those the same way. The shim leaves the `@file` argument and the file as the caller wrote them. It reads the file the way zig 0.17.0 does (`std.process.Args.IteratorGeneral` with `#` comments and single quotes), and right after the `@file` argument adds the respelled form of each `-L` and `-F` in it that the rule above keeps. This only adds: zig still reads the file and still drops the re-rooted original, so a kept directory is searched after the file's other directories rather than among them. An `@file` named inside the file is not followed, and one the shim cannot read is left to zig. A Linux target's `@file` is not read at all. `-Wl,-L<dir>` and `-Xlinker -L<dir>` are out of reach: zig 0.17.0 itself refuses them ("unsupported linker arg") before any search.

Repeated `-framework X` and `-lX` (or `-l X`) arguments are de-duplicated, keeping the first. zig's Mach-O linker otherwise links the dylib twice, and dyld aborts with `duplicate linked dylib CoreFoundation`. This is for darwin targets only: on Linux a repeated static library can matter, so it is left alone.

### `-bundle`

node-gyp links a macOS module as `c++ -bundle -undefined dynamic_lookup … -o Release/<name>.node <objects>`. zig 0.17.0 does not link a Mach-O bundle: it links `-bundle` as an executable, so a plain `-bundle` fails with `undefined symbol: _main`, and with `-undefined dynamic_lookup` its Mach-O linker crashes with a segmentation fault. kottera's W3-A3 found this on macOS 26.6.1 and reproduced it with zig invoked directly, no shim. So for a darwin target the shim passes each `-bundle` to zig as `-shared`. zig links that as an `MH_DYLIB`, which `dlopen` loads as it loads a bundle: node `require()`s it, and its undefined symbols resolve against node under `-undefined dynamic_lookup`.

The output type is what changes. The module is an `MH_DYLIB` with an install name (its `-o` path), not an `MH_BUNDLE`, so a loader that accepts only a bundle (the long-deprecated `NSCreateObjectFileImageFromFile`) would not load it. Only a bundle can link against an executable's symbols with `-bundle_loader <exe>`, so the shim refuses `-bundle` beside `-bundle_loader` rather than link a dylib that ignores it. The repair it names is `-undefined dynamic_lookup`, which resolves those symbols at load time instead. It also refuses a `-bundle` inside an `@file`, because it never rewrites the caller's file. `-Wl,-bundle` and `-Xlinker -bundle` are left alone: zig 0.17.0 refuses them itself (`unsupported linker arg`). `TestZigBundleOnDarwin` links node-gyp's line through the shim, then `dlopen()`s the module from a host program and `require()`s an N-API addon from node. Its control arm first asserts that bare zig does not link node-gyp's line.

## Compiles to /dev/null

`zig cc` does not honour `-o /dev/null`: it writes the object into the working directory instead, `<basename>.o` for a source file (`a.o` for `-c a.c -o /dev/null`, one combined `a.o` for `-c a.c b.c -o /dev/null`, `a.s` under `-S`) and `./-.o` for stdin input. Go's compiler probes run `cc … -c - -o /dev/null`, and other build tools probe a flag by compiling a file to `/dev/null`. So whenever a compile's output is `/dev/null` (`-o /dev/null` or `-o/dev/null`), whatever its input, the shim gives zig a fresh output under `$TMPDIR` instead (`zig-cc-shim-out-*.o`, `.s` under `-S`, `.i` under `-E`). kottera's bare-host measurement W1-A2 found the leak for every input with zig 0.17.0 on x86_64 Linux. W1-C6 reproduced it on darwin/arm64 for a Linux target.

Go also identifies its compiler with `cc -### -x c -c -`, run in the package's directory. clang only prints the commands it would run, but `zig cc` runs them and writes `./-.o`. So a stdin compile under `-###` that names no output gets one under `$TMPDIR` too. Go reads only the version line of that output, so the compiler's identity does not change. The integration tests found this: a cgo `go test -race` of a kottera checkout through the shim left a `-.o` in the checkout.

The shim replaces itself with zig, so it cannot remove these outputs after zig writes them. But `execve` keeps the process ID, so each output is named for the process that writes it, `zig-cc-shim-out-<pid>-<random><suffix>`, and every compile that creates one first removes the earlier ones whose process has exited and that were last written more than ten minutes ago (`shim.TempOutputGrace`, which covers a process the shim cannot see, such as one in another PID namespace sharing `$TMPDIR`). An output named the v0.1.0 way, `zig-cc-shim-out-<random><suffix>`, is removed on age alone. So `$TMPDIR` holds at most one output per running compile plus those of the last ten minutes, not one per compile ever run. Each output is still created exclusively under a random name, so concurrent compiles never share one. `TestZigDevNullOutputsBounded` runs this with a real zig.

The shim has already become zig by then, so it cannot remove that file. It is small, and it lives in the temporary directory.

## x86-64 micro-architecture levels

gcc and clang spell the x86-64 levels `-march=x86-64`, `-march=x86-64-v2`, `-march=x86-64-v3` and `-march=x86-64-v4`, and the Rust `cc` crate passes `-march=<target-cpu>` in that spelling. zig 0.17.0 rejects it (`unknown CPU: 'x86'`) and wants `x86_64`, `x86_64_v2`, `x86_64_v3` and `x86_64_v4`. For an x86_64 target the shim translates those four values to zig's spelling. Any other `-march` value is passed through untouched. Nothing is rewritten for an aarch64 target.

## A caller's --target

The Rust `cc` crate passes `--target=<triple>` to every compile, in LLVM's spelling, and zig 0.17.0 rejects that spelling. kottera's bare-host measurements found it with both crates they built: W1-A3 on x86_64 Linux (`--target=x86_64-unknown-linux-gnu`: `unable to parse target query … UnknownOperatingSystem`) and W1-A4 on macOS arm64 (`--target=arm64-apple-macosx`: `unknown architecture: 'arm64'`).

The shim already supplies the target, so a caller's target can only restate it or contradict it. For a compile role the shim reads every `--target=T`, `--target T` and `-target T`:

- If T names the role's own architecture and OS, the shim drops it, and zig sees only the shim's `-target`. Architectures are read in any of `x86_64`/`amd64` and `aarch64`/`arm64`; the OS as `linux` or as `darwin`, `macos` or `macosx`, with or without a vendor (`unknown`, `pc`, `apple`), and with or without a version (`arm64-apple-macosx13.0`, `x86_64-linux-gnu.2.17`). A Linux T may name the `gnu` ABI. A version in T is dropped with it: the glibc floor and the macOS minimum come only from `CC_TOOLCHAIN_TARGET_GLIBC` and `CC_TOOLCHAIN_TARGET_MACOS`.
- If T names another architecture, OS or ABI (`aarch64-unknown-linux-gnu` to an x86_64 role, `arm64-apple-ios`, `x86_64-unknown-linux-musl`, `arm64e-apple-macosx`), or is not a triple the shim can read, the shim refuses. The refusal names T, the role's target and, when one exists, the `<triple>-cc` or `<triple>-c++` role that builds T. The shim never builds for its own target when the caller asked for another, and never lets the caller's target replace its own.

A `--target` that is another flag's value (`-Xclang --target=…`) is left alone. `-mmacosx-version-min` is passed through: W1-A4 found zig accepts it and the shim's minimum still wins.

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

## The release

A release is cut by pushing a tag matching **`v*`** (for example `v0.1.0`); nothing else publishes one. The tag workflow (`.github/workflows/release.yml`) builds the shim for darwin/arm64, darwin/amd64, linux/amd64 and linux/arm64 and publishes a GitHub release of this repository with these assets, where `<tag>` is the pushed tag:

| Platform (`<os>/<arch>`) | Asset |
|---|---|
| `darwin/arm64` | `zig-cc-shim-<tag>-darwin-arm64.tar.gz` |
| `darwin/amd64` | `zig-cc-shim-<tag>-darwin-amd64.tar.gz` |
| `linux/amd64` | `zig-cc-shim-<tag>-linux-amd64.tar.gz` |
| `linux/arm64` | `zig-cc-shim-<tag>-linux-arm64.tar.gz` |
| all | `SHA256SUMS`, in `sha256sum`'s format (`<hex>  <asset>`) |

So a toolchain definition keys each `url` and `sha256` by `<os>/<arch>`, and takes each `sha256` from the release's `SHA256SUMS` (kottera ADR-155 D7).

Each tarball is shaped for kottera's `pkg/archive`, unpacked with `dest = "cc"` and `strip_components = 0`. It holds the directory `bin/`, the shim as `bin/<first role>`, and every other role as a hard link to it, all with mode 0755. There are no absolute names, no `..`, no symlinks, and no other files. Unpacked beside `cc/zig/`, `cc/bin/<role>` runs and finds `cc/zig/zig`. `internal/release`'s tests check this against the real shim build, and every release build checks each tarball before it writes it.

**Two builds of one commit are the same bytes.** The build pins everything that would otherwise vary:

- the Go build: `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=true` (the commit is stamped, which `--tera-describe` reports), and `-ldflags=-buildid=`. `GOFLAGS` and `GOEXPERIMENT` are cleared, `GOAMD64=v1` and `GOARM64=v8.0` are pinned, and `GOTOOLCHAIN=local` stops Go from fetching another toolchain. The Go version is the one `go.mod` names, which CI installs;
- the tar: USTAR, in a fixed entry order, with owner and group 0 and no names. Every entry's mtime is the commit's time;
- the gzip: no name, no time, best compression.

A build of a checkout with uncommitted changes is refused, because its digests would describe no commit. The same check CI runs is one command:

```
go run ./cmd/release build -check -out dist
```

It builds the whole release, then builds it again with an empty build cache, and fails if any byte differs. It writes the release into `dist/`, which git ignores. Add `-allow-modified` to try it on uncommitted work. On a tag, a pull request or a manual run, CI runs this on both self-hosted runners and then `go run ./cmd/release compare <macOS build> <Linux build>`, which fails unless both hosts built identical bytes. Only a pushed tag goes on to `go run ./cmd/release publish`, which creates the release as a draft, uploads every asset, then publishes it. It never replaces an existing release.
