# Roadmap

Reliability came first. What follows is ordered by the value it would
add to an identity's use of its memory, and nothing here is promised
for a date.

- **Pinned notes.** A `pinned` flag that automatic reclaim skips, so a
  standing agreement or an open question is never displaced; explicit
  eviction stays the only way out. A store full of pinned notes refuses
  a write rather than losing one.
- **The cold end.** A mode of `recent` that lists the least recently
  used notes, so an identity can look at what it is about to lose
  without touching it.
- **Collision candidates at write time.** `store` returns the notes
  whose content overlaps the new one, so the identity can update
  instead of duplicating. The identity judges; the tool makes the
  overlap visible.
- **Normalized matching.** Case folding is the only normalization
  today; Unicode-equivalent forms should match without rewriting the
  stored text.
- **Semantic retrieval as a companion.** Embedding-based recall belongs
  in a companion plugin, in the shape of the SDK's memory-embeddings
  example, rather than in this bounded working set.

Deliberately not planned: automatic surfacing of notes into a
conversation, and any authority derived from a note. Retrieval is
evidence the identity weighs, never an instruction.
