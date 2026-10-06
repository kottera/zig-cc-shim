version = "v1"

# Implementation work in kottera/zig-cc-shim: the dogfood deployment's
# code-implementation-direct, with this repository's description and nothing
# else (kottera ADR-155 D3, ADR-147 D8). Everything not declared here — the
# harness and its instructions, the inputs, the record, the postconditions,
# the volumes and the toolchains — is the base's, and follows an edit to the
# base with no edit here.
#
# The description names how work here is verified, because the base's
# instructions defer to "the repository's instructions" for that, and this
# repository's gate is `go run ./cmd/gate` (README.md, "The gate"), not
# kottera's `make gate`.
#
# No `toolchains` line: the base's list is inherited until Wave 4 of
# PLAN-2026-10-hermetic-c-toolchain declares this repository's own.

work_order_type "zig-cc-shim-implementation" {
  based_on    = "system/work-order-types/code-implementation-direct"
  description = "Implement a change to kottera/zig-cc-shim end-to-end: branch, change, verify with the repository gate `go run ./cmd/gate` (it prints `gate: ok`; README.md, \"The gate\"), then open and merge the pull request."
}
