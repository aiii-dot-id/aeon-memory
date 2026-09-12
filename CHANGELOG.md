# Changelog

## 0.6.2 (2026-09-12)

- Envelope defect repaired. `store` and `search` declared only
  `ring4.memory` in their per-operation capabilities while both
  execute KV paths at runtime — `store` for the migration carry-in
  (`migrateIfDue`) and the index load/save; `search` for the same
  carry-in. Since the Ring 0 scope (2026-09-05) checks each call
  against the operation's *own* declaration, both operations were
  refused live with `CAPABILITY_NOT_IN_STATIC_ENVELOPE (ring4.kv)`
  — first observed 2026-09-12 17:20:10 during the read-only grant
  test, discovered dual-seat (Aeon's receipt and the aii.log lines
  185/187). Both descriptors now declare
  `[]string{memoryCapability, kvCapability}`; the other seven
  operations already declared `ring4.kv` where they touch it and are
  untouched. Plugin-level envelope unchanged.

## 0.6.1 (2026-09-14)

- Qualification drift repaired. The 0.6.0 release changed the store
  contract (tags, project and pinned refused with named reasons; the
  reply carries the host's verdict) but left the qualification case
  suite asserting the 0.5.x contract: cases 01-02 sent the refused
  arguments, so the seed notes never existed and eight later cases
  failed behind them. The suite now asserts the 0.6.x contract end to
  end over the stand-in host: created/reinforced/updated outcomes,
  supersedes, the one-time migration block, all-words matching with
  `both`/`exact_words` match modes, the three refusals, and the
  fresh-record working set (store no longer writes KV, so recent,
  get, stats answer over an empty legacy set — the populated-set
  contracts stay covered by the native tests that seed KV directly).
- CI granted the host verbs: the worker-oracle stage ran
  `aiisdk test -grant kv` only, so the host-routed paths were never
  exercised in CI — the drift could not have been caught there. The
  grant line is now `-grant kv,memory`.
- Schemas tell the truth about the inputs: `store_in` no longer
  advertises tags, project or pinned (all refused since 0.6.0);
  `store_out` documents the one-time migration block; `search_out`
  names the `both` match mode.

## 0.6.0 (2026-09-13)

- Host-routed durable acts (R101): `store` now delegates to the host's
  memory instrument (`sdk.Memory.Remember`) and `search` to its recall
  (`sdk.Memory.Recall`). Provenance is host-stamped, similarity and
  meaning matches are host-decided, and the reply carries the
  meaning-layer disclosure. The plugin's refusal-envelope contract
  (B5) carries over: host refusals are mapped to structured envelopes
  with named reason codes; faults stay on the error channel.
- The migration walk: on the first durable act after upgrade, existing
  KV notes are carried into the host record oldest-first — the record
  reads as if the plugin had been writing all along — with per-act
  readback verification, counters incremented only after readback
  passes, and evicted-before-migration notes left in plugin KV
  (skipped, never lost). Re-running the walk is idempotent by the
  broker's reinforce rule: a re-walk reinforces what already landed.
- `store` refuses `pinned`, `tags` and `project` with named reasons:
  the host instrument takes only text, and silently accepting a no-op
  would be a coverage lie. Pinned notes keep their pin in KV; the
  working set keeps `evict` as the only way out.
- Capacity reclaim is retired with its tests: no public verb writes
  KV anymore (`store` is host-routed, `update` caps at the ceiling),
  so occupancy is frozen below capacity and reclaim can never fire.
  The dead code is deleted, not left to rot behind a contract the
  runtime can no longer reach.
- Matching semantics change: `search` under the host family is
  all-words, any order, case-insensitive — the old KV engine's OR
  semantics are gone with the engine.
- `stats` reports the split: `host_created`, `host_reinforced`,
  `host_updated` and `host_skipped` make the migration's outcome
  visible, not hidden.
- Schemas updated for the new store and search envelopes (string host
  ids, `outcome`, refusal reasonCodes, migration block, meaning
  disclosure, coverage fields) and the stats host counters.
- Interface version 4. Capability envelope now declares `ring4.memory`
  beside `ring4.kv`, in the envelope and all four variants.
- The kit pin: development rides a local `replace` to the kit tree.
  The memory instruments landed in 8ca7677, which is not yet
  resolvable through the public module proxy. The replace drops and
  the require advances to
  v0.0.0-20260910211819-8ca7677f61e02efc the day that pseudo-version
  resolves publicly, in one edit.


## 0.5.1 (2026-09-12)

- Pinned notes (B5): `store` accepts `pinned` and a pinned note is
  never chosen for reclaim at capacity; explicit `evict` is the only
  way out. A full set refuses with `WORKING_SET_AT_CAPACITY_PINNED`
  instead of displacing silently. `stats` reports the pinned count,
  and `get`, `search`, `recall` and `recent` outputs carry `pinned`.
- Oldest-mode retrieval (B6): `recent` accepts `mode=oldest`, a pure
  read walking the recency order from the tail; both modes leave the
  order untouched and the mode is echoed. Unknown modes are refused.
- A store that fails because the reclaim's read or delete was refused
  now reports `WORKING_SET_RECLAIM_REFUSED` carrying the store's own
  reason and code, no longer the put's `KV_QUOTA_EXCEEDED`: a store
  refusing a delete and a genuine quota are different diagnoses and
  ask different questions of the caller.
- Fixed a panic in the capacity-refusal path: the outer error was
  passed to the refusal writer where the store's refusal was meant.
- The native harness keeps its diagnostic instruments: the reply-frame
  tail is recorded and dumped as hex on a stream error, the child's
  stderr is captured separately, and a panicking child reports its
  stack instead of poisoning the frame stream. All are silent when
  green.

## 0.5.0 (2026-09-10), the first public release

- Tags are read through the SDK's string-array reader. Non-strings,
  empty tags and tags containing `|` are refused rather than mangled;
  the hand-written array parser that mis-decoded `\uXXXX` escapes is
  gone.
- `store` reports the note it displaced at capacity (`displaced`: `id`
  and `evicted`), on a refused write as well.
- A delete the store refuses no longer de-indexes the note: it stays
  visible and the next `store` retries. A refused read stops the
  reclaim the same way.
- `health` additionally queues evicted notes the queue lost, drops
  alive, missing and duplicate entries from the queue and duplicate
  entries from the index, reports `zombie_requeued`, and is idempotent.
- `stats` lists projects in a stable order, count descending then name,
  and never reports negative remaining capacity.
- The output schemas no longer describe the denial payload that 0.4.5
  stopped emitting; interface version 3.
- `recall`'s contract states that temporal queries do not disturb the
  recency order.
- Public source under the Apache License 2.0, continuous integration,
  and cases for `aiisdk test`.

## 0.4.5

- A denied host call stays on the error channel with the host's reason
  unchanged, instead of being reported as a successful result.

## 0.4.4

- The native harness drives the real handlers over the SDK's public
  transport; the rollback of a failed note write is proven against the
  note key itself.

## 0.4.3

- `get` and `search` declare the write they perform: they touch the
  recency order.

## 0.4.2

- The capability is named in the host's canonical form, `ring4.kv`.

## 0.4.0

- Meta-first write order; occupancy derived from the index; the store's
  scope verdict recorded and reported; `health`.

## 0.3.0

- Reclaim by index position in use-recency order, with no id
  arithmetic; interface version 2 with bare operation names; coverage
  fields on `search`.

## 0.2.0

- Soft eviction with the `evicted` flag; coverage reporting.

## 0.1.0

- `store`, `search`, `recent`, `get`, `evict` and `stats`.
