# zig-cc-shim

A small shim that sits in front of `zig cc` / `zig c++` so that zig can serve as the hermetic C toolchain `cc` for kottera's native builds (kottera ADR-155, D3). It presents the command-line interface that Go's cgo, Rust's linker hook and node-gyp expect, and pins the target so that no host header or library leaks into a build. This repository is private until further notice; the implementation arrives through pull requests against `main`.
