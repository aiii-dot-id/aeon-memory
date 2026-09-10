# Changelog

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
