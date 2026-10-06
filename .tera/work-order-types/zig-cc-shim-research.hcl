version = "v1"

# Research in kottera/zig-cc-shim — a spike or a measurement of zig, cgo or a
# target — producing a report: the dogfood deployment's code-research, with
# this repository's description and nothing else (kottera ADR-155 D3,
# ADR-147 D8). Everything not declared here is the base's, and follows an
# edit to the base with no edit here.
#
# The description names this repository's gate, `go run ./cmd/gate`
# (README.md, "The gate"), for a spike that builds or tests here, rather than
# kottera's `make gate`.
#
# No `toolchains` line: the base's list is inherited until Wave 4 of
# PLAN-2026-10-hermetic-c-toolchain declares this repository's own.

work_order_type "zig-cc-shim-research" {
  based_on    = "system/work-order-types/code-research"
  description = "Investigate a question about kottera/zig-cc-shim, zig or the targets it pins, and produce a structured research report. A spike that builds or tests here is verified with the repository gate `go run ./cmd/gate`, not kottera's `make gate`."
}
