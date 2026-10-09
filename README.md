# id.aeon.memory

A working memory for an AII OS identity, as a WebAssembly plugin. Notes go into
the host's memory record, which recalls them by their words or, when the
identity's provider names an embeddings model, by their meaning; a bounded
working set in the plugin key-value store holds notes kept before 0.6.0. Its nine
operations (`store`, `search`, `recent`, `get`, `update`, `recall`, `evict`,
`stats`, `health`) are described by the JSON schemas in `schemas/`. It needs two
grants: `ring4.memory`, the host's memory record, and `ring4.kv`, the plugin
key-value store.

Build with Go 1.25 or newer and TinyGo 0.42 or newer:

```sh
go tool aiisdk build      # dist/id.aeon.memory.wasm; ./build.sh is the same TinyGo build
go tool aiisdk package    # dist/id.aeon.memory-<version>.aiiospkg, unsigned
```

Copyright (c) AI Identity Incorporated. Licensed under Apache-2.0; see LICENSE.
