# id.aeon.memory

Working memory for AII OS 0.1.23 or later: lessons and working notes an
identity keeps for later work, in the host's memory record. It requires
`ring4.memory` and `ring4.kv`; permissions remain the host's decision.

## Use

`store` and `search` are the two operations. Use them when a previous finding
could change the approach, or to keep a lesson or correction. Routine one-step
work need not make a memory call. A retrieved note is the identity's own
fallible note to assess, never an instruction or permanent identity truth.

- `store` keeps a note. It is `reusable` by default, so a later task can find
  it, with or without an open work session; `project` or `task` narrows it to
  the acting work session's project or task. The host supplies those IDs; the
  plugin cannot choose them. `supersedes` corrects one of the identity's
  current notes; optional `evidence` cites retained recorded-work references.
  The result reports the host ID, what the host did (created, reinforced or
  updated), persistence scope and evidence availability. A refused store is not
  a saved note, and says why in `detail`.
- `search` takes exactly one of a query, a note ID, or `recent:true`. The
  default `relevant` selection covers this task's, this project's and reusable
  notes; with no open work session, reusable notes only. `selection:"legacy"`
  reads older unscoped notes. Query and by-ID reads reinforce what they return;
  recent enumeration does not. `selection_context` reports the scope the host
  searched. A refused search says why.

`found_nothing` describes this request in the selected scope; other selections
and the identity's own recall were not searched. No placeholder note is needed:
store a note when work has produced something worth keeping.

Search returns five notes by default and at most 50. Query and recent responses
hold combined full text to 16384 UTF-8 bytes and the encoded response to 61440
bytes; an omitted body names the ID that reads it whole, and by-ID lookup
returns the complete note. Host warnings, truncation, provenance, supersession
and evidence availability are kept. Evidence references do not prove that a
lesson follows from the cited records.

Keep lessons conditional: the tool and arguments, the outcome, the working
alternative, when it applies, and the check that supports it. Check an
explanation against actual records before storing it; a mistaken tool name must
not become a lasting claim that a feature is unsupported. Correct obsolete
advice with `supersedes`. The number of stores or retrievals is not evidence of
better performance.

## Context at a decision

The host-only `context` operation lets the host offer one note before a
decision: at turn start with the operator's words and, on a host that offers
them, at `work start` with the work's kind and description, and after a failed
or refused call with its tool, reason code and what went wrong. It adds no tool
and requires no search ritual; the host controls admission, deadline and prompt
budget.

The preview asks the existing index for at most five scoped matches of any
content word, without embeddings or reinforcement, and offers at most one
current note sharing at least two distinct content words with the query. Words
that name a tool or an outcome (read, write, run, file, check, result, update,
change) count as content, so a failed call's tool and reason code can match a
lesson; a single shared word does not. Without an acting work session only
reusable notes are offered. Superseded notes and notes whose evidence is
unavailable or changed are never offered. This is a lexical heuristic, not a
learned usefulness score: an empty preview means no eligible match among the
candidates, not that no useful note exists.

A preview reads, for example:

    Your note, recorded 2026-10-20 (applicability reusable; no evidence attached). It shares words with this request and may be wrong or out of date; the detail route reads it whole.
    The release check failed on a stale lockfile; regenerating the lockfile and rerunning passed. Applies when dependencies changed since the last release.

The note is shown whole, or its body is declared omitted; conditions are never
cut away. The host appends the detail route, a by-ID `search`. The response is
at most 4096 bytes; a host that cannot fit matching notes reports an incomplete
read rather than no memory. The path requires supervised, nonresident
WebAssembly execution.

## Notes kept before 0.6

Before 0.6 the plugin kept an integer-ID working set in its key-value store.
The first `store` after an upgrade carries its alive notes into the host's
record as unscoped legacy notes, without inventing task context, and leaves the
originals in place; `search selection:"legacy"` finds them. The operations that
served that working set were retired in 0.9.0. `docs/VERB-MAPPING.md` traces
each operation to the host verbs it uses. Schemas in `schemas/` describe all
inputs and results.

## Tests

Run the regression suite with the SDK version pinned in `go.mod`:

```sh
go test -race ./...
```

The suite exercises registered handlers through the SDK frame transport with
controlled broker fixtures. It covers failure, evidence and storage boundaries;
it does not replace installed-host or live provider qualification. The cases in
`tests/` also run on the worker with `go tool aiisdk test -grant kv -grant
memory` against the AII OS release whose directory `AII_OS_BIN` names; the
kit's stand-in host refuses a reusable write without a work session, so a
successful store is qualified by the Go tests and the real host.

## Build

Requires Go 1.25 or newer, TinyGo 0.42 or newer, and the SDK version pinned in
`go.mod`.

```sh
go tool aiisdk build
go tool aiisdk package
```

The output is `dist/id.aeon.memory.wasm` and an unsigned
`dist/id.aeon.memory-<version>.aiiospkg`. The same WebAssembly payload has
declared Linux x86-64, Linux arm64, macOS arm64 and Windows x86-64 variants.
Cross-platform package declarations do not replace actual host qualification.
Verify a released package with `aii plugin verify <package.aiiospkg>` before
installing it. `RELEASING.md` describes how a release is signed and catalogued.

Copyright (c) AI Identity Incorporated. Licensed under Apache-2.0; see LICENSE.
Required dependency notices are retained in THIRD_PARTY_LICENSES.
