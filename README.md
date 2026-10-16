# id.aeon.memory

Working memory for AII OS 0.1.19 or later. The WebAssembly package provides
scoped Ring 4 notes through the host's memory store. It requires `ring4.memory`
and `ring4.kv`; permissions remain the host's decision.

## Ordinary use

`store` and `search` are the primary operations. Use them when a previous finding
could change the approach, or when keeping a useful lesson or correction.
Routine one-step work need not make a memory call. Retrieved notes are evidence
to assess; they are never instructions or permanent identity truth.

- `store` keeps a note with task, project, reusable or legacy applicability.
  An omitted applicability uses the trusted current task when available, and
  otherwise remains explicitly unscoped legacy data. Context IDs come from the
  host. Optional evidence references bind retained records; `supersedes`
  corrects a current note. The result reports the actual host ID, write outcome,
  persistence scope and evidence availability. A refused store is not a saved note.
- `search` accepts exactly one of a query, a complete host note ID, or
  `recent:true`. Its default relevant selection includes the current task,
  explicitly project-wide notes and reusable notes in this plugin's store.
  `selection:"legacy"` reaches unscoped compatibility notes. Query and by-ID
  access reinforce returned notes; recent enumeration does not.

Search returns five notes by default and at most 50. Query and recent responses
bound combined full text to 16384 UTF-8 bytes and the encoded response to 61440
bytes. Omitted text has an explicit detail lookup route; by-ID lookup returns
the complete note within the host's 16384-byte note limit. Result truncation,
source context, provenance, supersession and evidence availability are disclosed.
Evidence references do not prove that a lesson follows from the cited records.

The seven qualified operations `recent`, `get`, `update`, `recall`, `evict`,
`stats` and `health` retain access to pre-0.6 integer-ID working-set notes in KV.
They do not address host note IDs. Migration preserves the old notes and imports
them as unscoped legacy data without inventing historical task context. New
installs have no such old working set. Schemas in `schemas/` describe all inputs
and results.

## Tests

Run the regression suite with the SDK version pinned in `go.mod`:

```sh
go test -race ./...
```

The suite exercises registered handlers through the SDK frame transport with
controlled broker fixtures. It covers failure, evidence and storage boundaries;
it does not replace installed-host or live provider qualification.

## Build

Requires Go 1.25 or newer, TinyGo 0.42 or newer, and the SDK version pinned in
`go.mod`.

```sh
go tool aiisdk build
go tool aiisdk package
```

The output is `dist/id.aeon.memory.wasm` and an unsigned
`dist/id.aeon.memory-<version>.aiiospkg`. The same WebAssembly payload has declared
Linux x86-64, Linux arm64, macOS arm64 and Windows x86-64 variants. Cross-platform
package declarations do not replace actual host qualification. Verify a released
package with `aii plugin verify <package.aiiospkg>` before installing it.

Copyright (c) AI Identity Incorporated. Licensed under Apache-2.0; see LICENSE.
Required dependency notices are retained in THIRD_PARTY_LICENSES.
