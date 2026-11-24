---
name: mem-engine
description: Use whenever the user asks to remember, save, recall or look up something from past sessions, or when you would otherwise write or read memory files. Covers the mem-engine MCP tools search, timeline, get and save.
---

# mem-engine memory

Long-term memory lives in the mem-engine MCP server, not in files.

## Recall

1. `search` with a short query.
2. `timeline` on an interesting id to see what happened around it.
3. `get` only for the ids worth reading in full.

## Save

- Use `save` instead of writing `.md` memory files (such as MEMORY.md).
- Pick a type: `fact`, `decision`, `gotcha`, `preference` or `reference`.

## Scope

- `scope: user` only for personal preferences that apply to every project. Everything else stays in the current project, the default.
- Never pass `project` to `search`, `timeline` or `get` unless the user explicitly asked about another project.
