# id.aeon.memory

Working memory for AII OS 0.1.20 or later. The WebAssembly package provides
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
  `selection_context` reports the host's actual selection operands. With no
  acting work session, `relevant` searches reusable notes only and reports
  `reason:"no_session"` without project/task IDs. A selected UI project alone
  does not supply that context. Task and project searches require the existing
  host-bound work context; the plugin cannot choose or invent its identifiers.

`found_nothing` describes this request in the selected plugin scope. It is not
proof that another project, another selection, core memory or the older KV
working set is empty. Recent search removes the query-text condition but keeps
the same scope. A by-ID miss retains the host's scope readback as well. An older
host may omit `selection_context`; the plugin leaves it absent rather than
inferring a scope. No synthetic placeholder note is needed: store a note only
when actual work has produced a useful finding worth retaining.

Search returns five notes by default and at most 50. Query and recent responses
bound combined full text to 16384 UTF-8 bytes and the encoded response to 61440
bytes. Omitted text has an explicit detail lookup route; by-ID lookup returns
the complete note within the host's 16384-byte note limit. Result truncation,
source context, provenance, supersession and evidence availability are disclosed.
Evidence references do not prove that a lesson follows from the cited records.

## Context before a decision

The host-only `context` operation lets an enabled, selected plugin offer a small
piece of relevant Ring 4 data before the first model decision. It does not add
another ordinary tool or require a search ritual. The host controls admission,
the deadline and the prompt budget; the plugin owns note selection and wording.

The preview requests at most five scoped matches of any content word from the
existing index. Requiring every word would miss a short lesson when the task
request also includes unrelated instructions. The host's context scope suppresses
embeddings and reinforcement; normal
query search retains its ordinary behavior. Preview performs no by-ID access,
migration or writes. It selects at most one current note
with at least two distinct content words in common with the request. This is a
conservative lexical heuristic, not a learned usefulness score. An absent acting
session limits the read to explicitly reusable notes. Superseded notes and notes
whose attached evidence is unavailable or changed are not offered automatically.

The response retains applicability, source and originating task, and discloses
whether structured evidence is attached. Even available references do not prove
a lesson correct. A complete note is presented, or its entire body is explicitly
omitted with an exact `search` route; truncating away conditions is unsafe. The
encoded response is at most 4096 bytes. No match produces an empty contribution.
That means no eligible match among the returned candidates: useful lower-ranked
notes, synonyms and single-word queries can be missed. Brief requests such as
"continue" can produce no candidate; child entry uses the child's complete goal.
The automatic broker response is bounded separately; if matching notes cannot
fit, it reports an incomplete read rather than claiming there is no memory.
Ordinary `search` remains available. This host-driven path requires supervised,
nonresident WebAssembly execution; it does not run inside native/resident plugins.

Keep lessons conditional: the observed tool and arguments, the returned outcome,
the working alternative, the circumstances where it applies, and the check that
supports it. Check a proposed explanation against actual records before storing
it. A mistaken tool name must not become a lasting claim that a platform feature
is unsupported. Correct obsolete advice with `supersedes`; do not treat the
number of stores, retrievals or accesses as evidence of improved performance.

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
