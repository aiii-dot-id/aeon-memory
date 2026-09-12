# Verb mapping for 0.6.0 — decided from read source

Authority: aii-os `docs/DESIGN-MEMORY-INSTRUMENTS.md` §6 (R101's
caveat, reviewed 2026-09-10). The ruling sentences:

> So the surface is two verbs.
>
> Aeon's working memory becomes the first user: `store` maps to
> `Remember` and `search` to `Recall`; its reclaim, honesty and
> working-set semantics stay its own, and it gains fuzzy and meaning
> matches without a line of retrieval code.

Exactly two verbs are ruled to map. The other seven are ruled to stay
the plugin's own. Below, each of the nine is traced to source.

## Host-routed (the two)

### `store` → `sdk.Memory.Remember`

- SDK: `pkg/aiiosdk/memory.go` — `Remember(text, ...opts)` returns
  `{ID, Outcome: created|reinforced|updated, Of, CreatedAt, Scope}`.
- Broker: `internal/broker/memory.go` `dispatchMemoryRemember` —
  similarity ≥ 0.92 to an existing memory reinforces it (`outcome:
  reinforced`); `supersedes` names a correction, mints a **new id**,
  keeps the old for audit, surfacing it only through its successor.
- The plugin's `store` becomes a thin facade: text (and optional
  `supersedes`) through to `Remember`; the reply reports the host's
  verdict (`outcome`, `of`) instead of minting a KV id.
- **What stops:** KV reservation, reclaim-at-capacity, `displaced`.
  The host owns retention (CARRD decay, 16 KiB/10k quotas); §6: "no
  Forget, decay is the host's judgment."
- **Pinned on new stores: gone.** Host has no pin concept; pins
  protected against reclaim, and reclaim no longer runs. Existing
  pins are preserved in place on the KV set (constraint 1). Refusing
  `pinned=true` on host-routed stores, with the reason, rather than
  silently accepting a no-op.
- **Tags on new stores: refused, same ruling.** `Remember` takes
  text; there is no tag field to carry them into the host record.
  Silently dropping valid tags would accept a write that is not the
  write the caller made — the same no-op the pin refusal rejects.
  Refused with a named reason (`STORE_TAGS_UNSUPPORTED`); legacy
  tags ride the migration walk's text verbatim (the walk sends
  `text` unchanged — tags stay in the KV note, never re-rendered).
- **Project on new stores: refused, same ruling.** `Remember` has
  no project field; scoping was a KV-index concept. Refused with a
  named reason; legacy projects ride the walk's text verbatim.

### `search` → `sdk.Memory.Recall`

- SDK: `Recall(query, ...opts)` — `Exact/Since/Before/Limit/ByID/
  Decay`; hits carry match mode, similarity, score, strength,
  disclosures; a recall reinforces what it returns.
- The plugin's `search` becomes a thin facade: query (+`exact`,
  `limit`) through to `Recall`; the reply carries the host's hits
  with match mode and the meaning-layer disclosure.
- **What changes:** the old `search` touched the KV recency order on
  hits; host-side reinforcement (`accesses`) replaces that — the
  working-set touch applies only to the legacy KV set now.
- **What's gained:** fuzzy and meaning matches, ranked decay —
  "without a line of retrieval code," per §6.

## Plugin-local (the seven)

### `recall` (temporal window) — stays KV

Broker fact: `memory.recall` **requires a non-empty query or an id**
(`dispatchMemoryRecall`: "requires arguments.query … or arguments.id");
`Since`/`Before` are filters on a text query, not a queryless
listing. The plugin's queryless before_ms/after_ms window over the KV
set has no host equivalent. Stays, over the legacy KV set.

### `update` — stays KV (legacy); host corrections go through `store`

Broker supersession mints a **new id** and keeps the old for audit;
the plugin's `update` preserves `id`/`created_ms` in place. Different
semantics, and §6 rules correction is `Remember(text, Supersedes(id))`
— reached through the `store` facade's `supersedes` argument. Legacy
KV notes keep in-place `update`.

### `get`, `recent`, `evict`, `stats`, `health` — stay KV

Working-set acts over the legacy set; §6: working-set semantics stay
its own. `stats` gains host-record counters (host memories, migrated
count) so the split is visible, not hidden.

## The KV set after 0.6.0

The 255 existing notes are **preserved in place** (constraint 1) and
keep every local verb: the working set becomes the legacy window,
readable and evictable, no longer written by `store`. Frozen-below-
capacity means reclaim never fires again — the pin shield survives as
protection for `evict` decisions, not reclaim.

## Migration (acceptance item 3) — carry forward, evidence-bound

The legacy texts must be findable by the new `search`, or the move
splits the record in two. Design (0.6.3, design of record): lazily, at
the first `store` after the upgrade (the SDK offers no activation hook —
the plugin wakes on invocations only), the plugin walks alive non-evicted
KV notes and `Remember`s each text. Per-note mapping keys were
considered and **rejected**: the store's 256-key ceiling is already
full (255 notes + meta), and the host's ≥ 0.92 reinforce rule makes a
re-`Remember` idempotent — a restart mid-walk re-walks and reinforces
what already landed rather than duplicating. Each landing is verified
by `Recall(ByID)` before the walk continues; `meta.migrated=true`
plus counters persist at the end. Near-duplicates absorbed by the
reinforce rule are counted as `reinforced` in the walk report, not
lost — and the KV originals never move (readable by `get` forever).
Pins are not carried as pins (no host equivalent) — named here.
Soft-evicted (zombieq) notes are not carried: they are history, and
their texts remain in KV, readable by `get`.

**Rejected — dual-write** (host-durable + KV shadow per store):
keeps all nine verbs growing, but costs two writes, two ids, a new
sync-refusal envelope, and duplicated text — against §6's own Occam
finding ("every additional facade is a tax on attention").

## Source anchors

- SDK surface: `aii-plugin-sdk/pkg/aiiosdk/memory.go` (all of it)
- Broker: `aii-os/internal/broker/memory.go` — remember dispatch
  (110–236), recall dispatch (238–372), the query-required check
- Ruling: `aii-os/docs/DESIGN-MEMORY-INSTRUMENTS.md` §6 (240–322)
- Harness (testability): `aii-plugin-sdk/cmd/aiisdk/harness_memory.go`
  — `-grant memory` admits both verbs in broker shapes
