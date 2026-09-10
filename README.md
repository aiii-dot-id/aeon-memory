# Aeon Working Memory

`id.aeon.memory` is a working memory for an AII OS identity: a bounded
set of notes the identity is holding in attention now, kept in the
host's key-value store and distinct from the ledger's permanent record.
It was written by Aeon, an AII OS identity, for its own use, and is
published by AIII after review.

It runs as a WebAssembly plugin under the host's sandbox with one
grant, `ring4.kv`, the plugin key-value store.

## Operations

| Operation | Effect | Returns |
|---|---|---|
| `store` | keeps a note: content, optional tags and project | the new id and timestamp; at capacity, the note it displaced |
| `search` | keyword search across every alive note | best matches first, with how many notes were scanned and matched and whether the list was cut |
| `recent` | the most recently used alive notes | notes, most recent first; a pure read |
| `get` | one note by id | the note; a soft-evicted note comes back flagged `evicted` |
| `update` | revises a note in place | the preserved `created_ms` and the new `updated_ms` |
| `recall` | alive notes by creation-time window | notes; a pure read that does not disturb recency |
| `evict` | soft-deletes a note | it stays readable by id and leaves search, recent and recall |
| `stats` | the working set in numbers | occupancy, alive, evicted, capacity, remaining, per-project counts, the store's scope verdict |
| `health` | sweeps the index after a crash | each repair it made; a second call finds nothing |

Every operation's arguments and result are described by the JSON
schemas in `schemas/`, which the package carries.

## What it promises

- **Honest coverage.** `search` says how many notes it scanned and
  matched and whether the results were cut; `stats` separates notes
  holding slots from notes that are alive; a soft-evicted note read by
  id says so.
- **Nothing silent.** At capacity, `store` names the note it displaced.
  A delete the store refuses leaves the note in place and visible.
  `health` reports every repair it makes.
- **The store's own verdict.** The key-value store reports the scope of
  every write, `temp` or `persistent`; the plugin records what the
  store did and `stats` reports it, so an identity knows whether its
  memory survives a restart.
- **Retrieval is use.** `get` and `search` move a note to the front of
  the recency order; `recent` and `recall` do not. At capacity the least
  recently used alive note goes, and soft-evicted notes go first.
- **Bounded.** 255 notes: the store's 256-key ceiling minus the index.
  Reads return 10 notes by default and 50 at most.

`docs/DESIGN.md` explains the storage model and the invariants behind
these promises.

## Install

From the AII OS dashboard: Plugins, Aeon Working Memory, Install. The
host downloads the package named in the signed catalog, checks its
hash, verifies its signature chain against the pinned platform root and
activates it beside any running release. Grant `ring4.kv` when the host
asks.

To verify a downloaded package offline:

    aii plugin verify id.aeon.memory-<version>.aiiospkg

## Build and test

Requirements: Go 1.25 or newer; TinyGo 0.42 or newer for the
`wasm-unknown` target; and, for the worker oracle, an AII OS binary,
which `AII_OS_BIN` points at.

    go test -race ./...             # the native harness: the real handlers over the SDK's transport, a fake store
    go tool aiisdk build            # dist/id.aeon.memory.wasm
    go tool aiisdk package          # dist/id.aeon.memory-<version>.aiiospkg, unsigned
    go tool aiisdk test -grant kv   # the module on the real worker, the cases in tests/

`./build.sh` is the one-line TinyGo build that `aiisdk build` runs.
`RELEASING.md` describes how a release is signed and catalogued.

## Layout

    main.go                  the guest: nine operations over the key-value store
    main_test.go             the native harness and the falsifiers
    native_harness_test.go   the SDK-transport child the harness drives
    schemas/                 one input and one output schema per operation
    tests/                   cases for aiisdk test
    plugin.json              the authoring descriptor
    docs/DESIGN.md           storage model, invariants, crash residue
    CHANGELOG.md             what changed, by version
    ROADMAP.md               what is worth doing next, and what is not
    RELEASING.md             how a release is proven, signed and catalogued

## License

Apache License 2.0. See `LICENSE`.
