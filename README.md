# mem-engine

A single-binary, local long-term memory for Claude Code that captures session observations and brings relevant context back into new sessions.

## Status: under development

## Why

Claude Code's native memory loads the whole `MEMORY.md` index into every session, and memory tools that summarize each event with an LLM spend tokens on every tool call. mem-engine captures without an LLM, opens each session with a handoff of about 300 tokens, and lets the agent fetch the rest through layered search (`search`, then `timeline`, then `get`). Memory stays local, isolated per project, and editable as Markdown in git or in a local viewer. It builds on ideas from [claude-mem](https://github.com/thedotmack/claude-mem) and [ai-memory](https://github.com/akitaonrails/ai-memory).

## Documentation

- [Why mem-engine](docs/why.md): the problems it solves, the projects it builds on, goals and non-goals
- [How it works](docs/how-it-works.md): architecture, capture, handoff, layered search, curated memory, viewer and concurrency, with flow diagrams
- [How it was built](docs/how-it-was-built.md): design, plan, test-driven implementation, reviews and CI

## Install

The hooks call `mem` by name, so whichever way you install it, the binary must be on your `PATH`. To use it as a Claude Code plugin, install the binary first and then see [Install as a Claude Code plugin](#install-as-a-claude-code-plugin).

### From a release

Each [release](https://github.com/andreabreu76/mem-engine/releases) has an archive per platform, `tar.gz` for macOS and Linux and `zip` for Windows, on `amd64` and `arm64`, plus `checksums.txt`. Download the archive for your platform and `checksums.txt`, verify the archive and put `mem` on your `PATH`:

```sh
sha256sum --ignore-missing -c checksums.txt   # macOS: shasum -a 256 --ignore-missing -c checksums.txt
tar -xzf mem-engine_<version>_linux_amd64.tar.gz
install -m 0755 mem ~/.local/bin/mem
mem version
```

On Windows, in PowerShell: `Expand-Archive mem-engine_<version>_windows_amd64.zip -DestinationPath "$HOME\bin\mem-engine"`, then add that folder to your user `PATH` with `[Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ";$HOME\bin\mem-engine", 'User')` and open a new terminal.

### With Go

Requires Go 1.26 or newer. The binary is pure Go (no cgo).

```sh
go install github.com/andreabreu76/mem-engine/cmd/mem@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`.

## Setup

`mem setup` registers the capture hooks in `~/.claude/settings.json` and the `mem-engine` MCP server in Claude Code. Other keys and hooks in that file are kept, existing `mem hook` entries are replaced, and the previous file is saved as `settings.json.bak`. The MCP server is registered with the `claude` CLI, and skipped when it is already registered or `claude` is not on `PATH`.

```sh
mem setup --plan   # print the hooks and the claude command that would run, change nothing
mem setup          # show the plan and ask before applying (--yes skips the prompt)
```

`mem setup --disable-native-memory` turns off Claude Code's native auto-memory instead (see [Migrating from native memory](#migrating-from-native-memory)).

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

Compares what Claude Code's native memory loads at the start of every session with what mem-engine costs instead. The native side is the project's `MEMORY.md` under `~/.claude/projects/<encoded repository root>/memory/` (the git root of `path`, groups and aliases do not apply), counted the way Claude Code loads it: the first 200 lines, at most 25 KB. The mem-engine side has two parts: the handoff a new session of the project would receive now (at most 1200 bytes) and the fixed cost of every session, the `tools/list` payload of the MCP server plus the name and description of the plugin skill, counted in full. The saving compares the native load with the sum of both. Tokens are estimated as bytes / 4. `path` defaults to the working directory and `--native` points to another `MEMORY.md`.

For a project with a 192-line `MEMORY.md` and one previous session:

```
native MEMORY.md: 16788 bytes loaded (~4197 tokens)
mem-engine handoff: 379 bytes (~94 tokens)
mcp tools + skill: 4448 bytes (~1112 tokens)
saving: ~2991 tokens per session (71%)
```

When the project has no `MEMORY.md`, only the handoff and fixed cost lines are printed. With a small `MEMORY.md` the fixed cost dominates and the saving is negative: mem-engine pays off once `MEMORY.md` loads more than about 5 KB, and its handoff stays capped while `MEMORY.md` keeps growing. The native file is read locally and nothing is sent anywhere, and `mem bench` never creates the data directory or the database: without `mem.db` it measures the minimal handoff of an empty project.

## Migrating from native memory

Claude Code's native auto-memory keeps one `.md` file per memory, with YAML frontmatter, plus a `MEMORY.md` index, under `~/.claude/projects/<encoded repository root>/memory/`. `mem import` turns those files into curated mem-engine memories without changing or deleting anything in the source directory.

```sh
mem import ~/.claude/projects/-home-dev-src-billing-api/memory --project /home/dev/src/billing-api
mem import ~/.claude/projects/-home-dev/memory --user
mem import <dir> --project billing-api --dry-run
```

`--project` takes a path (resolved like any other project, groups and aliases included) or the name or key of a project mem-engine already knows. `--user` imports into the user scope, visible in every project. Without either flag, `mem import` uses the user scope only when `<dir>` is the native memory directory of your home; any other directory needs `--project` or `--user`. `--dry-run` lists the titles that would be imported and the target, and writes nothing.

Every `*.md` except `MEMORY.md` becomes one memory: the title is the native `name` with dashes turned into spaces, the summary is the `description`, the type is the native type (`metadata.type` or a top-level `type`) kept as is, the body is copied unchanged and the memory is tagged `imported` and `native:<source file>`. Two files with the same `name` are both imported, the second one under a suffixed wiki file, with a note. Links such as `[[deploy-window]]` (by `name` or by source file name) are rewritten to the memory id when they point to a memory imported into the same target, in this run or an earlier one; with duplicate names they point to the first source file in name order. Each run also rewrites links in memories imported earlier that can be resolved now. Links inside fenced code blocks are left alone.

```
imported 12, skipped 1
unresolved [[old-runbook]] in deploy-window.md
notes.md: skipped, no frontmatter
```

Files without frontmatter, without a `name`, without a `description` or with a type mem-engine does not accept are skipped with a note. Running the import again is safe: a source file already imported into the target (tag `native:<source file>`) is skipped with the note `already imported`, so a run that stopped halfway is finished by running it again. Exit code 0 on success, 1 on error (after printing what was imported until then), 2 on bad arguments.

Once the import looks right, turn off the native memory so Claude Code stops loading `MEMORY.md` next to the mem-engine handoff:

```sh
mem setup --disable-native-memory   # show the change and ask before applying (--yes skips the prompt, --plan only shows it)
```

It sets `"autoMemoryEnabled": false` in `~/.claude/settings.json` and keeps every other key, with the same confirmation, `settings.json.bak` backup and atomic write as `mem setup`. Hooks and the MCP server are not touched in this mode. Running it again when the native memory is already off changes nothing.

## Projects

A project is identified by its key, the git root of the working directory (or the prefix of a `[[group]]`). When a repository moves or two keys turn out to be the same project, two commands fix it:

```sh
mem project merge <from> <to> [--yes]
mem project alias <old-key> <new-path>
```

`mem project merge` moves everything recorded under `<from>` into `<to>`: each memory file is rewritten in the wiki directory of `<to>` with the new `project` (same id, tags and dates, a suffixed file name when the name is taken) and committed in both wiki repositories, and the observations and sessions move in one database transaction. `<from>` and `<to>` are project names or keys; a name shared by several projects is refused with the list of keys. Memory files in the wiki directory of `<from>` that are missing from the index move too. `<to>` must be a project mem-engine already knows: open a Claude Code session in it first, or pass its key. The command shows how many memories, observations and sessions will move and asks before applying (`--yes` skips the prompt), then prints `moved N observations, M memories`. `<from>` is removed from the project list only after its wiki directory holds no memory file; a file there that is not a memory stops the merge with its name, to be moved or deleted by hand. If a step fails, the memories moved until then stay valid and running the same command again continues where it stopped. The `user` scope cannot be merged, nor a project into itself.

`mem project alias` keeps recording a repository under an existing key after it moved, for example from `/home/dev/src/billing-api` to `/home/dev/src/acme/billing-api`:

```sh
mem project alias /home/dev/src/billing-api /home/dev/src/acme/billing-api
```

It appends an `[[alias]]` block to `config.toml` (created with mode 0600 when missing, existing content and mode kept, previous file saved as `config.toml.bak`). The result is checked before it is written, atomically: when the file cannot take the block (for example, aliases declared as an inline array), nothing is written and the alias has to be added by hand. Both arguments must be absolute paths (`~/` is expanded), and a second alias for the same path is refused. Exit code 0 on success, 1 on error, 2 on bad arguments, for both commands.

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

Everything stays on your machine: nothing captured is sent to any external service. Captured observations and curated memories (`save`, `update`, the viewer and `mem import`) go through the same redaction before any write to the database, the spool, the log or the wiki: text inside `<private>...</private>` tags is removed, and builtin patterns redact common secrets (tokens, keys, passwords in URLs) in titles, summaries, bodies and tags. Add your own patterns with `redact_extra`.

The builtin list does not cover `Authorization: Bearer` headers or `.env` style assignments such as `DB_PASSWORD=` and `API_SECRET=`. Add them to `config.toml` if they show up in your sessions; a named group `secret` redacts only that part of the match:

```toml
redact_extra = [
  '(?i)authorization:\s*bearer\s+(?P<secret>[A-Za-z0-9._~+/=-]+)',
  '(?i)\b\w*(?:PASSWORD|SECRET)\w*\s*[=:]\s*(?P<secret>[^\s"\\]+)',
]
```

`forget` deletes the memory file and its index entry, but the wiki of each project is a local git repository, so earlier versions of the file stay in its history. To purge them, close Claude Code and `mem ui`, then rewrite the history of that project's wiki, for example with [git-filter-repo](https://github.com/newren/git-filter-repo):

```sh
cd ~/.mem-engine/wiki/billing-api-1a2b3c
git filter-repo --force --invert-paths --path deploy-token.md
```

or drop the whole history of that wiki while keeping the current files:

```sh
cd ~/.mem-engine/wiki/billing-api-1a2b3c
rm -rf .git && git init -q && git add -A && git commit -qm "mem: reset history"
```
