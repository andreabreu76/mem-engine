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

`mem setup` registers the capture hooks in `~/.claude/settings.json`. Other keys and hooks in that file are kept, existing `mem hook` entries are replaced, and the previous file is saved as `settings.json.bak`.

```sh
mem setup --plan   # print the hooks that would be registered, write nothing
mem setup          # show the plan and ask before writing (--yes skips the prompt)
```

## Health

```sh
mem doctor
```

Prints one line per check, `ok`, `warn` or `FAIL`, followed by the check name and a detail:

```
warn  binary          PATH resolves mem to /usr/local/bin/mem, running /home/dev/go/bin/mem
ok    config          defaults (no config.toml)
ok    data-dir        /home/dev/.mem-engine
ok    database        /home/dev/.mem-engine/mem.db (schema 1, fts5 ok)
ok    spool           empty
ok    capture-errors  none in the last 24h
ok    hooks           /home/dev/.claude/settings.json
```

Checks: `binary` (the `mem` on `PATH` is the one running), `config` (`config.toml` parses), `data-dir` (writable), `database` (opens, schema version, FTS5), `spool` (events waiting to be written), `capture-errors` (hook failures in the last 24 hours) and `hooks` (registered and up to date). `mem doctor` never creates the data directory or the database. It exits with 1 only when a check fails.

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
