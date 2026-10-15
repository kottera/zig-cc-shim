version = "v1"

# This vault builds on major v1 of the installation `dev-pipeline`: the
# built-in `tera plugin add builtin:dev-pipeline` installs (ADR-161 D12).
# zig-cc-shim-implementation and zig-cc-shim-research derive from its
# code-implementation-direct and code-research by declared address, and a
# declared address into a plugin needs this declaration or the plan refuses it
# `builds_on_missing` (ADR-158 D1).
builds_on "dev-pipeline" {
  major_version = "v1"
}
