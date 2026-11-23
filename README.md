# mem-engine

A single-binary, local long-term memory for Claude Code that captures session observations and brings relevant context back into new sessions.

## Status: under development

## Install

Requires Go 1.26 or newer. The binary is pure Go (no cgo).

```sh
go install github.com/andreabreu76/mem-engine/cmd/mem@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`, since the hooks call `mem` by name.

## Setup

`mem setup` registers the capture hooks in `~/.claude/settings.json` and the `mem-engine` MCP server in Claude Code. Other keys and hooks in that file are kept, existing `mem hook` entries are replaced, and the previous file is saved as `settings.json.bak`. The MCP server is registered with the `claude` CLI, and skipped when it is already registered or `claude` is not on `PATH`.

```sh
mem setup --plan   # print the hooks and the claude command that would run, change nothing
mem setup          # show the plan and ask before applying (--yes skips the prompt)
```

To register the MCP server by hand:

```sh
claude mcp add --scope user mem-engine -- mem mcp
```

## Install as a Claude Code plugin

This repository is also a Claude Code plugin marketplace. The `mem-engine` plugin registers the same capture hooks (`mem hook <Event>`), the `mem-engine` MCP server (`mem mcp`) and a short skill that tells the agent to use the memory tools instead of writing memory files.

```sh
claude plugin marketplace add andreabreu76/mem-engine
claude plugin install mem-engine@mem-engine
```

Inside a session, `/plugin install mem-engine --marketplace andreabreu76/mem-engine` does both in one step.

The plugin only wires Claude Code to the binary: `mem` must still be installed and on `PATH` (see Install). Use either the plugin or `mem setup`, not both, or every hook runs twice; `mem doctor` warns about it in the `hooks-duplicated` check.

## MCP tools

`mem mcp` serves six tools over stdio. The current project is `$CLAUDE_PROJECT_DIR`, or the working directory when it is unset.

Retrieval is layered to keep context small: `search` first, `timeline` for context around an interesting hit, `get` only for the ids worth reading in full.

| Tool | Parameters | Returns |
|------|------------|---------|
| `search` | `query`, `type?`, `since?` (`YYYY-MM-DD` or `7d`), `project?`, `limit?` (10, at most 50) | one line per item: `<id> <date> <type> <title> \| <summary>` |
| `timeline` | `id`, `before?` (5), `after?` (5), `project?` | the same lines for the items of the same session around `id`, which is marked with `>` |
| `get` | `ids`, `project?` | header and body of each id; the whole response is limited to 20000 bytes and ids that do not fit are listed in a final `[output truncated ...]` line |
| `save` | `type`, `title`, `summary`, `body`, `tags?`, `scope?` (`project` or `user`) | the new memory id |
| `update` | `id`, `type?`, `title?`, `summary?`, `body?`, `tags?` | the updated id |
| `forget` | `id` | the deleted id |

Titles are cut at 80 characters and summaries at 100 in `search` and `timeline` lines. Recommended memory types are `fact`, `decision`, `gotcha`, `preference` and `reference`. A memory saved with `scope: user` is visible in every project.

```
m-3f9a1c2b4d5e 2026-11-20 decision Invoices use UTC | store invoice dates in UTC, local time broke month-end totals
o-1042 2026-11-21 tool Bash: go test ./... | ok  billing-api/internal/invoice 0.412s
```

By default `search`, `timeline` and `get` only see the current project plus `user` memories. `project` takes another project's name or key (for example `billing-api` or `/home/dev/src/acme/billing-api`) and the tool descriptions tell the agent to pass it only when you ask about another project. An unknown or ambiguous name is an error, never a search across all projects, and an id outside the scope answers "not found". `update` and `forget` only act on memories of the current project or `user`. Errors the agent can fix (invalid type, missing title, unknown project, id not found) come back as they are; any other failure answers `<tool> failed, see mem.log` and the full error goes to `logs/mem.log`.

## Health

```sh
mem doctor
```

Prints one line per check, `ok`, `warn` or `FAIL`, followed by the check name and a detail:

```
warn  binary            PATH resolves mem to /usr/local/bin/mem, running /home/dev/go/bin/mem
ok    config            defaults (no config.toml)
ok    data-dir          /home/dev/.mem-engine
ok    database          /home/dev/.mem-engine/mem.db (schema 1, fts5 ok)
ok    spool             empty
ok    capture-errors    none in the last 24h
ok    hooks             /home/dev/.claude/settings.json
ok    hooks-duplicated  none
ok    mcp               mem-engine registered with claude
```

Checks: `binary` (the `mem` on `PATH` is the one running), `config` (`config.toml` parses), `data-dir` (writable), `database` (opens, schema version, FTS5), `spool` (events waiting to be written), `capture-errors` (hook failures in the last 24 hours), `hooks` (registered and up to date), `hooks-duplicated` (mem hooks in `settings.json` while the `mem-engine` plugin is also enabled) and `mcp` (`claude mcp get mem-engine` succeeds, skipped when `claude` is not on `PATH`). With the `mem-engine` plugin enabled, `hooks` and `mcp` report `provided by plugin mem-engine@<marketplace>` instead of asking for `mem setup`. `mem doctor` never creates the data directory or the database. It exits with 1 only when a check fails.

## Benchmark

```sh
mem bench [path] [--native file]
```

Compares what Claude Code's native memory loads at the start of every session with the handoff mem-engine injects instead. The native side is the project's `MEMORY.md` under `~/.claude/projects/<encoded repository root>/memory/` (the git root of `path`, groups and aliases do not apply), counted the way Claude Code loads it: the first 200 lines, at most 25 KB. The mem-engine side is the handoff a new session of the project would receive now (at most 1200 bytes). Tokens are estimated as bytes / 4. `path` defaults to the working directory and `--native` points to another `MEMORY.md`.

```
native MEMORY.md: 18432 bytes loaded (~4608 tokens)
mem-engine handoff: 612 bytes (~153 tokens)
saving: ~4455 tokens per session (97%)
```

When the project has no `MEMORY.md`, only the handoff line is printed. A handoff larger than the native file shows a negative saving. The native file is read locally and nothing is sent anywhere, and `mem bench` never creates the data directory or the database: without `mem.db` it measures the minimal handoff of an empty project.

## Viewer

```sh
mem ui [--port N]
```

Serves a local web viewer and prints the address to open:

```
mem ui: http://127.0.0.1:47600/?token=<random token>
```

The viewer listens on `127.0.0.1` only, on `ui_port` (47600 by default) or `--port`. The token is new on every run and the page and the API need it (only the static script and stylesheet are served without it), requests with another `Host` are refused, and edits and deletes also need the page's own `Origin`. The page takes the token from the address bar into memory and removes it from the URL, so after a reload open the printed URL again. When the port is taken, `mem ui` exits with an error; stop it with Ctrl+C.

What it shows:

- a live feed of captured observations, refreshed every 2 seconds while the tab is visible;
- search over memories and observations, filtered by project, type and date;
- an item page with the full body and the session timeline around it;
- an edit form for memories (type, title, summary, tags, body), saved to the wiki like `update` (a memory whose body is too large to show in full is not editable here), and delete buttons for memories and observations, both asking for confirmation.

The project selector starts at "All projects"; pick one to narrow the feed and the search to that project plus `user` memories.

The badge at the top is green when the spool is empty and no capture failed in the last 24 hours. It turns red when events are waiting in the spool or hooks failed to capture, shows both counts, and its tooltip carries the last capture error. `mem doctor` gives the details.

## Configuration

Data lives in `$MEM_ENGINE_HOME`, or `~/.mem-engine` when the variable is unset:

```
config.toml   optional settings
mem.db        SQLite database (WAL)
wiki/         curated memory
logs/         mem.log
spool/        events kept when the database was unavailable
```

`config.toml` is optional. Every key and its default:

```toml
retention_days = 90                 # observations older than this are deleted
max_observation_bytes = 4096        # observations are truncated at this size
ui_port = 47600                     # local viewer port, bound to 127.0.0.1
skip_tools = ["*mem-engine*"]       # tool names (glob) that are not captured
redact_extra = []                   # extra regular expressions to redact
redact_disable_builtin = false      # turn off the builtin secret patterns

[[group]]                           # everything under prefix is one project
name = "acme"
prefix = "/home/dev/src/acme"

[[alias]]                           # record a repository under another project key
path = "/home/dev/src/acme/billing-api-old"
key = "/home/dev/src/acme/billing-api"
```

No groups or aliases are defined by default.

## Privacy

Everything stays on your machine: nothing captured is sent to any external service. Text inside `<private>...</private>` tags is removed before anything is stored, and builtin patterns redact common secrets (tokens, keys, passwords in URLs) before any write to the database, the spool or the log. Add your own patterns with `redact_extra`.
