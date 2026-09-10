# Releasing

A release is a signed package attached to a GitHub release of this
repository, and an entry in the AII OS plugin catalog.

1. Bump `version` in `plugin.json` and add the entry to `CHANGELOG.md`.
2. Prove the tree: `go test -race ./...`, then `go tool aiisdk build`,
   `go tool aiisdk package` and `go tool aiisdk test -grant kv` against
   an AII OS binary named by `AII_OS_BIN`.
3. Record the package hash `aiisdk package` prints. The package is
   canonical: the same tree yields the same bytes.
4. Signing. Packages are signed at the platform tier, T3, by the AIII
   platform authority in the ceremony that also signs AII OS releases.
   The keys never leave that ceremony and are no part of this
   repository. The signed `.aiiospkg` must verify offline with
   `aii plugin verify` before anything is published.
5. Publish a GitHub release tagged `v<version>` with the signed
   `.aiiospkg` as its asset.
6. `go tool aiisdk publish` prints the catalog entry: the package URL,
   its hash and its size. Submit it to `aiii-dot-id/plugin-catalog`,
   whose index is signed by the same authority. Hosts refresh the
   catalog hourly and offer the new version as an update.

Releases are immutable. A mistake gets a new version.
