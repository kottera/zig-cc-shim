# This repository's deployment manifest. It maps the repository's own
# .tera/ onto its vault on the dogfood deployment, so `tera config plan`, run
# here, plans this vault and nothing else. The vault is tracked by the
# deployment (a `config-v*` tag a person pushes is what goes live), so
# `tera config apply` from here is refused `vault_tracked` once adopted.

target {
  context = "macmini"
}

vault "zig-cc-shim" {
  source = "."
}
