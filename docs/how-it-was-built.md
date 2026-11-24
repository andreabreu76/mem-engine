# How mem-engine was built

mem-engine was built in Claude Code, following a design-first workflow: a written design, an implementation plan split into small tasks, test-driven development for every task, and several layers of review before anything reached `main`. The design and the plan are in [`docs/superpowers/`](superpowers/), and the decisions that are not obvious from the code are in [`DECISIONS.md`](../DECISIONS.md) and [`GOTCHAS.md`](../GOTCHAS.md).

## 1. Design

The [design](superpowers/specs/2026-11-23-mem-engine-design.md) started from the problems described in [Why mem-engine](why.md) and from what worked and failed in claude-mem and ai-memory. It fixes the intent, the goals, the non-goals (the ways the project fails even if it technically works) and the constraints: Go, one binary, pure-Go SQLite with FTS5, everything local, MIT license.

Three architectures were compared:

| | How it works | Decision |
|---|---|---|
| A. One binary, no resident process | Hooks call `mem hook <event>`, which writes straight to SQLite; the agent opens the MCP server over stdio; `mem ui` runs on demand | **Chosen**: nothing to install as a service, nothing that can be down without anyone noticing |
| B. Resident service | A worker on a local port receives events over HTTP | Rejected: one more service to monitor, and when it is down capture is lost silently |
| C. Hybrid | Use the service when it is up, write directly otherwise | Rejected: two code paths to keep correct |

The price of A is that the viewer feed polls instead of being pushed, and that several processes write to the same files. Most of the engineering effort went into the second point (see [Concurrency](how-it-works.md#concurrency)).

## 2. Plan

The [plan](superpowers/plans/2026-11-23-mem-engine.md) breaks the design into 22 tasks in six phases, each phase ending with something usable:

| Phase | Delivers |
|---|---|
| 1 | Database, capture by hooks, retention, `mem setup`, `mem doctor` |
| 2 | Curated memory in a git wiki and the MCP tools |
| 3 | Session-start handoff and `mem bench` |
| 4 | The local viewer, `mem ui` |
| 5 | Claude Code plugin, marketplace and multi-platform releases |
| 6 | Native memory import, project merge and alias |

Every task names its files, its tests and its review focus.

## 3. Implementation

Each task was implemented by a separate agent with a fresh context and reviewed right after, before the next one started:

- **Tests first.** Every behaviour starts as a failing test, and every bug found in review becomes a failing test committed before its fix. The history shows the pairs: `test(...)` followed by `fix(...)`.
- **Review per task.** Each task got a code review focused on what the plan flagged for it: error handling, concurrency, scope isolation, redaction. Findings were fixed in the same task.
- **A final review of the whole branch** with a stronger model, which caught, among others, a handoff that could show the last session of another project and curated memory that skipped redaction.

## 4. External review

Before the pull request, the full diff went through other models from other vendors, run locally, because a reviewer from the same model family tends to share the blind spots of the author. That round found a redaction bypass through JSON escapes, a race between `mem reindex` and the creation of a new project, a race between `forget` and a project move, and a `get` response that could exceed its size cap. Each one was fixed test-first.

## 5. Continuous integration on three systems

CI runs `go vet` and `go test` on Linux, macOS and Windows. Until the first pull request, Windows had only been cross-compiled, never tested, and the first run there found real defects that macOS and Linux could not show:

- two processes draining the spool at the same time collided on file renames; the drain is now exclusive between processes;
- opening the database took the write lock even when no migration was needed, so a hook could fail while another one was writing; opening now only reads the schema version;
- the per-commit fsync made writes slow enough to exhaust the busy timeout; the WAL now runs with `synchronous=NORMAL`.

The same run also exposed test-only assumptions (the home directory comes from `USERPROFILE` on Windows, and Git converted line endings on checkout). These are recorded in [`GOTCHAS.md`](../GOTCHAS.md).

## Principles that held throughout

- **Minimal first.** The smallest design that solves the concrete problem; no speculative abstraction or extension points.
- **Fail loudly.** Every failure path either recovers (spool, retry, minimal handoff) or leaves a trace that `mem doctor` reports.
- **Local and private by default.** Nothing captured leaves the machine, and redaction happens before the first write.
- **Measure the cost.** `mem bench` compares the native memory load with mem-engine's on your own data, so the main claim of the project can be checked rather than trusted.
