# Design

## What the store offers

The plugin key-value store offers `put`, `get` and `delete` on string
keys, with a ceiling of 256 keys per plugin and no way to list or scan
them. Every `put` answers with the scope it was written under: `temp`,
wiped at the next activation, or `persistent`.

## Storage model

    meta    {"next_id":N,"recent":"42 41 40","zombieq":"7 3","kv_scope":"persistent"}
    n/<id>  {"id":1,"content":"…","tags":"a|b","project":"x",
             "created_ms":N,"updated_ms":N,"evicted":false}

One key, `meta`, is the index of record. It holds:

- `next_id`, the id the next note takes; ids are never reused;
- `recent`, every occupying note's id in use-recency order, most
  recently used first. The window equals the capacity, so every note
  that holds a key is in it, and no note is invisible to `search`,
  `recent` or `stats`;
- `zombieq`, the soft-evicted notes in eviction order, oldest first;
- `kv_scope`, the scope the store last reported.

Occupancy is the length of `recent`. It is never stored beside the
index: one referent per fact.

Values are encoded by hand and read back through the SDK's `Object`
reader, because the module is built without `encoding/json`. Id lists
are space-separated strings and tags are joined with `|`, which is why
a tag may not contain that character.

## Write order

`store` writes the index first and the note second. A crash between the
two leaves a stale index entry: an id whose key does not exist. Every
reader skips it, reclaim drops it and `health` reports it. The other
order would leave an orphan key: a note that exists in the store and
that no operation could ever reach, holding a slot forever.

`evict` writes the note first, flagged, and the queue second. A crash
between the two leaves an evicted note the queue does not know about.
`health` finds it during the index walk and queues it.

## Retention

Retrieval is use. `get` and `search` move a note to the front of the
recency order, and so does `update`. `recent` and `recall` are pure
reads.

At capacity, `store` reclaims exactly one slot before writing:

1. the oldest queued zombie whose key still exists;
2. otherwise the least recently used alive note, the tail of the index.

The victim is always indexed, so its key and its index entry go
together, and `store` reports it as `displaced`. A read or delete the
store refuses stops the reclaim with nothing de-indexed: a note that
still holds a key stays visible, the write proceeds and is either
accepted or refused by the store with a structured reason, and the
next `store` tries again.

Soft eviction keeps the note readable by id, so a change of mind does
not erase history; hard deletion happens only under capacity pressure.

## Health

`health` walks the index and the queue and repairs what a crash can
leave. It drops index entries whose key is gone and duplicate entries,
queues evicted notes the queue lost, and drops from the queue notes
that are alive, missing or duplicated. It reports each count and is
idempotent: a second call finds nothing.

## Refusals

A denied host call is not a result. It stays on the error channel,
where the host's envelope carries the reason unchanged. The one
structured refusal is the store's own: a write refused for quota or
value size comes back as `stored: false, refused: true` with the
store's reason code, so the caller learns that the substrate declined
rather than that the plugin failed.

## Limits

- 255 notes.
- Keyword search is case-insensitive substring matching per term. It is
  not normalized for Unicode equivalence, and it is not semantic.
- `recent`, `search` and `recall` return 10 notes by default and 50 at
  most.
