# Changelog

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
