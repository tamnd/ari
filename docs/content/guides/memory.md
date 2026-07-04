---
title: "Colony memory"
description: "How the ant remembers across sessions: a per-project colony.db it writes only through folding, the pinned index it carries every turn, and the recall, remember, and forget tools it reaches it with."
weight: 65
---

`ARI.md` is the memory you write. Colony memory is the memory the ant writes for itself: the facts, decisions, and lessons it accumulates as it works a repo, kept in a per-project database it carries from one session to the next. Where an `ARI.md` house rule is a standing instruction, a colony memory is a learned one, and the ant earns it rather than being handed it.

## Where it lives

Each project gets its own `colony.db`, stored under ari's home directory outside the repo, never inside the working tree. Nothing about your memory is committable, and nothing leaks into a diff. `ari doctor` enforces this: a stray `colony.db` in the repo is flagged critical, because a memory file in a commit is a memory file in everyone's clone.

The database is versioned. A new ari migrates an old `colony.db` forward on the first run and never throws it away, so upgrading keeps every memory you have. If a colony was written by a newer ari than the one you are running, doctor stops you before you touch it.

## How the ant remembers

The ant reaches its memory with three tools.

- `remember` proposes a memory. It does not store one. A proposal is queued as a candidate for the next fold, so a single stray thought cannot poison the next turn; only a consolidation that weighed it against what is already known makes it recallable.
- `recall` searches memory for what bears on a query. It runs a hybrid of full-text and vector search, ranks by relevance, recency, and importance together, and returns a small ranked list tagged fresh or stale. Recalling a row reinforces it, so the memories the ant actually uses stay fresh and the ones it never touches fade.
- `forget` archives a memory by id. It is never a delete: the row leaves recall and the pinned index but stays in the file, so a retirement is always reversible.

## Folding

Between turns the consolidator folds: it takes the candidates the ant proposed, merges near-duplicates, draws reflections from clusters that share an anchor, and retires what a file change invalidated. Folding is the only writer of live memory, which is what keeps memory clean under pressure.

Two properties matter to you. Merging takes the strongest single note's importance, never the sum, so saying the same thing ten times does not lift its rank; repetition buys no weight. And a fold summarizes a whole cluster in one cheap-tier model call, so the cost scales with the number of distinct ideas, not the volume of notes.

## The pinned index

The ant does not recall on every turn. The load-bearing memories, the pinned ones, are rendered into a compact index the ant carries at the head of every prompt, and it recalls only for what the index does not already cover. That index is rebuilt only at a fold boundary, never mid-turn, so the prompt prefix stays byte-identical across a run of turns. That stability is what lets the model's prompt cache pay off: an index that shifted every turn would throw the cache away and make every turn pay full price.

## Editing memory by hand

Memory is the ant's, but you have the final say over it.

```bash
ari memory export             # render the namespace to markdown on stdout
ari memory export --out mem.md
ari memory import mem.md       # read your edits back in
```

Export renders a namespace to markdown you can open in your editor. On import, an edited body updates the row and marks it read-only, a block you add becomes a new memory, and a block you delete archives its row. A read-only row is the highest provenance there is, so the consolidator never rewrites what you edited by hand.

## The memory panel

Press `ctrl+r` in the TUI to open the memory panel: it shows the live pinned index the ant is carrying, searches archival memory with the same ranking the loop uses, and tails the fold log so you watch consolidation happen. A forget from the panel is not a privileged delete; it routes through the same permission prompt a forget the model asked for would.
