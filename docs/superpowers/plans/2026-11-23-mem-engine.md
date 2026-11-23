# mem-engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Memória de longo prazo para Claude Code em um binário Go (`mem`): captura por hooks sem LLM, busca em camadas por MCP, handoff curto no SessionStart, visualizador local, plugin e importador da memória nativa.

**Architecture:** Sem processo residente. Cada hook chama `mem hook <Evento>`, que grava direto no SQLite (WAL, `busy_timeout`) e cai num spool JSONL se o banco estiver ocupado. Memória curada é Markdown com frontmatter num repo git por projeto em `wiki/`, e o SQLite é índice reconstruível. MCP por stdio (`mem mcp`) e visualizador sob demanda (`mem ui`) usam o mesmo binário.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` v1.63.0 (FTS5, sem CGO), `github.com/modelcontextprotocol/go-sdk` v1.8.0, `github.com/BurntSushi/toml` v1.6.0, `go.yaml.in/yaml/v3` v3.0.6, `github.com/gofrs/flock` v0.13.2, stdlib `net/http`, `embed`, `log/slog`.

**Spec:** `docs/superpowers/specs/2026-11-23-mem-engine-design.md` (decisões fechadas, não reabrir).

## Global Constraints

- Módulo `github.com/andreabreu76/mem-engine`, binário `mem` em `cmd/mem`, `CGO_ENABLED=0`.
- Código e identificadores em inglês, sem comentários. Strings do produto (CLI, handoff, descrições MCP, UI) e README em inglês, por ser projeto público.
- Repositório público e genérico: nenhum nome de cliente, empresa, pessoa ou caminho real de máquina em código, teste, fixture, exemplo ou doc. Exemplos usam `billing-api`, `acme`, `/home/dev/src/...` ou `t.TempDir()`.
- Segredos de teste montados por concatenação (`"ghp_" + strings.Repeat("a", 36)`), nunca literais que pareçam reais.
- Conventional Commits, branch `main`, sem Co-Authored-By, sem menção a IA, mensagens em português.
- Diretório de dados: `$MEM_ENGINE_HOME` ou `~/.mem-engine/`, com `config.toml`, `mem.db`, `wiki/`, `logs/`, `spool/`.
- SQLite em WAL, `busy_timeout` 2000 ms, migração sob `BEGIN IMMEDIATE`, tabela `schema_version`; banco mais novo que o binário: recusa abrir para escrita.
- Hooks de captura terminam com exit 0 mesmo com erro; erro vai para `logs/mem.log` e para `capture_errors`; escrita que falha vai para o spool já redigida.
- `<private>...</private>` removido e redação de segredos aplicada antes de qualquer gravação (banco, spool ou log).
- Observação truncada em 4096 bytes por padrão, com corte em fronteira UTF-8.
- Retenção de observações: 90 dias por padrão; GC no SessionStart no máximo uma vez a cada 24 h.
- Handoff com corte rígido em 1200 caracteres; tokens estimados por `bytes/4`.
- Busca restrita ao projeto atual mais escopo `user`; outro projeto só com `project` explícito. Vale para `search`, `timeline`, `get` e para a API do visualizador.
- Visualizador só em `127.0.0.1`, porta padrão 47600, token aleatório por execução, checagem de `Host` e `Origin`.
- Nenhum envio de conteúdo capturado para serviço externo.
- Licença MIT, titular `mem-engine contributors`.

## Review Focus

1. Payload de hook malformado, gigante (saída de ferramenta de vários MB), com `tool_response` objeto em vez de string ou com bytes não UTF-8: o hook não entra em pânico, sai 0, grava truncado ou registra o erro. Teste em Task 7.
2. `cwd` inexistente, symlink (ex.: `/tmp` vs `/private/tmp` no macOS) ou dentro de `.git/`: chave de projeto estável e igual para o mesmo repo. Teste em Task 3.
3. Consulta de busca com aspas, `-`, `:`, `*`, `NEAR`, `AND` ou vazia: nunca vira erro de sintaxe do FTS5. Teste em Task 13.
4. `get` ou `timeline` com id de outro projeto, sem `project` explícito: não devolve o conteúdo. Teste em Task 13 e Task 14.
5. Arquivo de memória editado à mão com frontmatter quebrado: `mem reindex` pula o arquivo, informa o caminho e indexa o resto. Teste em Task 12.

## File Structure

```
cmd/mem/                      main.go (dispatch), um arquivo por subcomando, run() testável
internal/config/              config.toml, defaults, caminhos
internal/project/             cwd -> Project (git common dir, grupos, alias), nome do diretório no wiki
internal/applog/              slog JSON em logs/mem.log com rotação simples
internal/redact/              <private>, padrões de segredo, truncamento UTF-8
internal/spool/               append e drain de JSONL
internal/store/               SQLite: schema, observações, sessões, memórias, busca, GC
internal/capture/             payload de hook -> Op, Handle() do `mem hook`
internal/wiki/                frontmatter, escrita atômica, lock, git
internal/memory/              save/update/forget/reindex (wiki + índice)
internal/handoff/             texto do SessionStart
internal/mcpserver/           ferramentas MCP
internal/ui/                  servidor HTTP, API, assets embutidos
internal/setup/               hooks e MCP no settings do Claude Code, memória nativa
internal/doctor/              checagens
internal/bench/               MEMORY.md nativo vs handoff
internal/importer/            importação da memória nativa
plugins/mem-engine/           plugin do Claude Code
.claude-plugin/marketplace.json
```

---

## Fase 1: banco, captura por hooks e `mem doctor`

Entrega: com `mem setup`, toda sessão do Claude Code passa a gravar prompts, ferramentas e paradas no SQLite; `mem doctor` diz se a captura está saudável.

### Task 1: Esqueleto do repositório e CI

**Files:**
- Create: `go.mod`, `LICENSE`, `README.md`, `cmd/mem/main.go`, `cmd/mem/main_test.go`, `.github/workflows/ci.yml`
- Modify: `.gitignore` (já existe com `.claude/settings.local.json` e `.worktrees/`)

**Interfaces:**
- Produces: `func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int` em `cmd/mem/main.go`; `var version = "dev"` (sobrescrito por `-ldflags "-X main.version=..."`). Subcomandos desconhecidos imprimem uso em stderr e retornam 2.

- [ ] **Step 1: Teste**

```go
func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.TrimSpace(out.String()) != "mem dev" {
		t.Fatalf("got %q", out.String())
	}
}
func TestRunUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"nope"}, nil, &out, &errb); code != 2 {
		t.Fatalf("code=%d", code)
	}
}
```

- [ ] **Step 2:** `go test ./cmd/mem/` falha (sem `run`).
- [ ] **Step 3:** `go mod init github.com/andreabreu76/mem-engine`; `main()` chama `os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`; dispatch por `switch args[0]`. `LICENSE` MIT com `Copyright (c) 2026 mem-engine contributors`. `.gitignore`: acrescentar `/mem` e `/dist/`. `README.md` com título, uma frase de propósito e seção "Status: under development". `ci.yml`: `go vet ./...` e `go test ./...` em `ubuntu-latest`, `macos-latest`, `windows-latest`, Go 1.26.
- [ ] **Step 4:** `go test ./... && go vet ./...` passa.
- [ ] **Step 5:** `git commit -m "chore: esqueleto do módulo, licença MIT e CI"`

### Task 2: Configuração

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:

```go
type Group struct { Name string `toml:"name"`; Prefix string `toml:"prefix"` }
type Alias struct { Path string `toml:"path"`; Key string `toml:"key"` }
type Config struct {
	Home                string   `toml:"-"`
	RetentionDays       int      `toml:"retention_days"`
	MaxObservationBytes int      `toml:"max_observation_bytes"`
	UIPort              int      `toml:"ui_port"`
	SkipTools           []string `toml:"skip_tools"`
	RedactExtra         []string `toml:"redact_extra"`
	RedactDisableBuiltin bool    `toml:"redact_disable_builtin"`
	Groups              []Group  `toml:"group"`
	Aliases             []Alias  `toml:"alias"`
}
func Load() (Config, error)
func LoadFrom(home string) (Config, error)
func (c Config) DBPath() string
func (c Config) WikiDir() string
func (c Config) LogDir() string
func (c Config) SpoolDir() string
func ExpandHome(p string) string
```

Defaults: `RetentionDays=90`, `MaxObservationBytes=4096`, `UIPort=47600`, `SkipTools=["*mem-engine*"]`. `Load` usa `$MEM_ENGINE_HOME` ou `~/.mem-engine`. Arquivo ausente não é erro. `Prefix` e `Alias.Path/Key` passam por `ExpandHome` e `filepath.Clean` no load.

- [ ] **Step 1: Testes** `TestLoadDefaultsWhenFileMissing` (valores acima, `DBPath()==filepath.Join(home,"mem.db")`), `TestLoadGroupsAndOverrides` (TOML com `retention_days = 30` e `[[group]] name="acme" prefix="~/src/acme/"`; espera prefixo expandido para `filepath.Join(userHome,"src","acme")`), `TestLoadInvalidTOMLFails`, `TestLoadUsesEnvHome` (`t.Setenv("MEM_ENGINE_HOME", dir)`).
- [ ] **Step 2:** `go test ./internal/config/` falha.
- [ ] **Step 3:** Implementar com `BurntSushi/toml`.
- [ ] **Step 4:** Testes passam.
- [ ] **Step 5:** `git commit -m "feat(config): config.toml com defaults, grupos e alias"`

### Task 3: Resolução de projeto

**Files:**
- Create: `internal/project/project.go`, `internal/project/project_test.go`

**Interfaces:**
- Consumes: `config.Config` (Groups, Aliases).
- Produces:

```go
type Project struct { Key, Name, Dir string }
var User = Project{Key: "user", Name: "user", Dir: "user"}
func Resolve(cwd string, cfg config.Config) (Project, error)
func DirName(name, key string) string
func Slug(s string) string
```

Regras, em ordem: `filepath.Abs` + `filepath.EvalSymlinks` (se o `cwd` não existe, sobe até o ancestral existente e reanexa o resto); grupo com o prefixo mais longo que contenha o caminho vence (`Key` = prefixo limpo, `Name` = nome do grupo); senão, ancestral mais próximo com `.git`: diretório → raiz é o ancestral; arquivo → ler `gitdir: X`, ler `X/commondir` (relativo a X), raiz = pai do common dir; sem `.git` → o próprio caminho. Caminho dentro de `.git/` resolve para a raiz do repo. Depois, alias com `Path == Key` troca `Key` por `Alias.Key`. `Name` padrão = base da raiz. `Dir = DirName(Name, Key)` = `Slug(Name) + "-" + hex(sha256(Key))[:6]`. `Slug`: minúsculas, não alfanumérico vira `-`, colapsa e apara `-`, máximo 40 caracteres. Sem chamar o binário `git` (latência do hook).

- [ ] **Step 1: Testes** (repos criados com `git init` e `git worktree add` em `t.TempDir()`; pular com `t.Skip` se `git` não estiver no PATH):
  - `TestResolveGitRoot`: `cwd` em `repo/a/b` → `Key == repo`, `Name == "repo"`.
  - `TestResolveWorktreeSharesKey`: worktree em outro diretório → mesma `Key` do repo principal.
  - `TestResolveNoGit`: diretório comum → `Key == cwd`.
  - `TestResolveGroupLongestPrefix`: grupos `acme` em `root/src/acme` e `acme-infra` em `root/src/acme/infra`; `cwd` em `root/src/acme/infra/x` → `Name == "acme-infra"`.
  - `TestResolveAlias`: alias `{Path: novo, Key: antigo}` → `Key == antigo`.
  - `TestResolveSymlinkStable`: symlink para o repo resolve para a mesma `Key`.
  - `TestResolveMissingCwd`: `repo/does/not/exist` → `Key == repo`.
  - `TestResolveInsideDotGit`: `repo/.git/hooks` → `Key == repo`.
  - `TestDirName`: `DirName("Billing API", "/home/dev/src/billing-api")` casa `^billing-api-[0-9a-f]{6}$` e é determinístico.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(project): chave por raiz comum do git, grupos e alias"`

### Task 4: Store, esquema e migração

**Files:**
- Create: `internal/store/store.go`, `internal/store/schema.go`, `internal/store/store_test.go`

**Interfaces:**
- Produces:

```go
const SchemaVersion = 1
var ErrSchemaTooNew = errors.New("database schema is newer than this binary")
type Options struct { BusyTimeout time.Duration }
func Open(path string) (*Store, error)
func OpenWith(path string, o Options) (*Store, error)
func (s *Store) Close() error
func (s *Store) Version(ctx context.Context) (int, error)
func (s *Store) CheckFTS5(ctx context.Context) error
```

DSN: `file:<path>?_pragma=busy_timeout(<ms>)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate`. `Open` usa 2 s. Migração numa transação imediata: lê `schema_version`; maior que `SchemaVersion` → `ErrSchemaTooNew`; menor → aplica migrações em sequência.

Esquema v1 (fica todo aqui; tasks seguintes só usam):

```sql
CREATE TABLE schema_version (version INTEGER NOT NULL);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE projects (key TEXT PRIMARY KEY, name TEXT NOT NULL, dir TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE sessions (id TEXT PRIMARY KEY, project TEXT NOT NULL, cwd TEXT, source TEXT, started_at INTEGER NOT NULL, ended_at INTEGER, end_reason TEXT);
CREATE TABLE observations (id INTEGER PRIMARY KEY, session_id TEXT NOT NULL, project TEXT NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, tool_name TEXT, files TEXT, created_at INTEGER NOT NULL);
CREATE INDEX observations_project_time ON observations(project, created_at);
CREATE INDEX observations_session ON observations(session_id, created_at);
CREATE VIRTUAL TABLE observations_fts USING fts5(title, body, content='observations', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TABLE memories (id TEXT PRIMARY KEY, project TEXT NOT NULL, type TEXT NOT NULL, title TEXT NOT NULL, summary TEXT NOT NULL, body TEXT NOT NULL, tags TEXT NOT NULL, path TEXT NOT NULL, source_session TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE VIRTUAL TABLE memories_fts USING fts5(title, summary, body, tags, content='memories', content_rowid='rowid', tokenize='unicode61 remove_diacritics 2');
CREATE TABLE capture_errors (id INTEGER PRIMARY KEY, event TEXT NOT NULL, message TEXT NOT NULL, created_at INTEGER NOT NULL);
```

Mais os triggers `AFTER INSERT/DELETE/UPDATE` padrão de external content para as duas tabelas FTS. Tempos em Unix segundos UTC. `files` e `tags` em JSON array.

- [ ] **Step 1: Testes** `TestOpenCreatesSchema` (`Version()==1`, `CheckFTS5()==nil`, `PRAGMA journal_mode` devolve `wal`), `TestOpenIsIdempotent` (abrir duas vezes), `TestOpenRefusesNewerSchema` (gravar `UPDATE schema_version SET version=99`, reabrir → `errors.Is(err, ErrSchemaTooNew)`), `TestConcurrentOpen` (10 goroutines abrindo o mesmo arquivo novo ao mesmo tempo, todas sem erro).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam (incluir `-race`).
- [ ] **Step 5:** `git commit -m "feat(store): SQLite em WAL com esquema versionado e FTS5"`

### Task 5: Redação e truncamento

**Files:**
- Create: `internal/redact/redact.go`, `internal/redact/redact_test.go`

**Interfaces:**
- Produces:

```go
type Redactor struct { /* compiled patterns */ }
func New(extra []string, disableBuiltin bool) (*Redactor, error)
func (r *Redactor) Clean(s string) string
func StripPrivate(s string) string
func Truncate(s string, max int) string
```

`Clean` aplica `StripPrivate` e depois os padrões. Padrões de fábrica e o rótulo da substituição `[REDACTED:<name>]`:
`github` (`gh[pousr]_[A-Za-z0-9]{36,}`, `github_pat_[A-Za-z0-9_]{22,}`), `slack` (`xox[abprs]-[A-Za-z0-9-]{10,}`), `aws` (`(AKIA|ASIA)[0-9A-Z]{16}` e `(?i)aws_secret_access_key\s*[=:]\s*\S+`), `anthropic` (`sk-ant-[A-Za-z0-9_-]{20,}`, testado antes de `openai`), `openai` (`sk-(proj-)?[A-Za-z0-9_-]{20,}`), `jwt` (`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`), `conn` (senha em `scheme://user:senha@`, troca só a senha), `pem` (bloco `-----BEGIN [A-Z ]*PRIVATE KEY-----` até o END). `extra` inválido é erro em `New`. `StripPrivate` remove `<private>...</private>` (case-insensitive, multilinha, não guloso); tag aberta sem fechamento remove até o fim. `Truncate` corta em fronteira de rune e anexa `\n[truncated N bytes]` quando `len(s) > max`, com resultado de no máximo `max` bytes mais o sufixo.

- [ ] **Step 1: Testes** tabela `TestCleanBuiltin` com um caso por padrão (token montado por concatenação, saída contém `[REDACTED:<name>]` e não contém o token); `TestCleanConnStringKeepsHost` (`postgres://app:` + `s3cr3t` + `@db.example.com:5432/x` → contém `db.example.com`, não contém `s3cr3t`); `TestCleanExtra`; `TestDisableBuiltin`; `TestStripPrivate` (inline, multilinha, sem fechamento); `TestTruncateUTF8` (`strings.Repeat("ã", 3000)` com max 4096 → `utf8.ValidString`).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(redact): remoção de <private>, segredos de fábrica e truncamento"`

### Task 6: Observações, sessões, erros e spool

**Files:**
- Create: `internal/store/observations.go`, `internal/store/observations_test.go`, `internal/spool/spool.go`, `internal/spool/spool_test.go`

**Interfaces:**
- Produces (store):

```go
type Observation struct {
	ID int64; SessionID, Project, Kind, Title, Body, ToolName string
	Files []string; CreatedAt time.Time
}
type SessionEvent struct {
	SessionID, Project, Cwd, Source, EndReason string
	At time.Time; End bool
}
type ProjectRow struct { Key, Name, Dir string }
func (s *Store) EnsureProject(ctx context.Context, p ProjectRow) error
func (s *Store) TouchSession(ctx context.Context, ev SessionEvent) error
func (s *Store) InsertObservation(ctx context.Context, o Observation) (int64, error)
func (s *Store) RecordCaptureError(ctx context.Context, event, msg string, at time.Time) error
func (s *Store) CaptureErrorsSince(ctx context.Context, since time.Time) (count int, last string, err error)
```

`Kind` ∈ `prompt`, `tool`, `tool_error`, `stop`, `compact_start`, `compact`, `session_start`. `TouchSession` faz upsert: cria com `started_at=At`; `Source` só grava se vazio; `End` grava `ended_at` e `end_reason`. `EnsureProject` é `INSERT OR IGNORE`.

- Produces (spool):

```go
func Append(dir string, v any) error
func Drain(dir string, apply func(json.RawMessage) error) (applied int, err error)
func Pending(dir string) (lines int, err error)
```

`Append` grava uma linha JSON em `dir/<pid>-<unixnano>.jsonl` com `O_APPEND|O_CREATE`. `Drain` reivindica cada `*.jsonl` renomeando para `*.draining-<pid>` (falha de rename = outro processo pegou, pular), aplica linha a linha e apaga o arquivo; se `apply` falha numa linha, regrava as linhas restantes num novo `.jsonl` e retorna o erro.

- [ ] **Step 1: Testes** store: `TestInsertObservationSearchableByFTS` (insere e acha via `SELECT rowid FROM observations_fts WHERE observations_fts MATCH 'nao'` para body com "não"), `TestTouchSessionUpsert` (start, depois end → `ended_at` e `end_reason` preenchidos, `source` preservado), `TestCaptureErrorsSince`, `TestConcurrentInserts` (8 goroutines, cada uma com seu `Store`, 50 inserts cada → 400 linhas). Spool: `TestAppendDrain` (3 appends, drain aplica 3 na ordem e o diretório fica vazio), `TestDrainPartialFailure` (falha na 2ª linha → 1 aplicada, `Pending` == 2), `TestDrainConcurrent` (dois `Drain` em paralelo, cada linha aplicada exatamente uma vez).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam com `-race`.
- [ ] **Step 5:** `git commit -m "feat(store): observações, sessões, erros de captura e spool JSONL"`

### Task 7: `mem hook` e captura

**Files:**
- Create: `internal/applog/applog.go`, `internal/capture/payload.go`, `internal/capture/build.go`, `internal/capture/handle.go`, `internal/capture/build_test.go`, `internal/capture/handle_test.go`, `cmd/mem/hook.go`
- Modify: `cmd/mem/main.go` (case `hook`)

**Interfaces:**
- Consumes: Tasks 2 a 6.
- Produces:

```go
// applog
func Open(dir string) (*slog.Logger, io.Closer, error)

// capture
type Payload struct {
	SessionID string `json:"session_id"`; TranscriptPath string `json:"transcript_path"`
	Cwd string `json:"cwd"`; HookEventName string `json:"hook_event_name"`
	Source string `json:"source"`; Prompt string `json:"prompt"`
	ToolName string `json:"tool_name"`; ToolInput json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`; Error string `json:"error"`
	DurationMS int64 `json:"duration_ms"`; LastAssistantMessage string `json:"last_assistant_message"`
	Trigger string `json:"trigger"`; CompactSummary string `json:"compact_summary"`; Reason string `json:"reason"`
}
type Op struct { Project store.ProjectRow; Session store.SessionEvent; Obs *store.Observation }
type Deps struct {
	Cfg config.Config; Log *slog.Logger; Now func() time.Time
	StoreOptions store.Options
	SessionStartOutput func(ctx context.Context, st *store.Store, op Op, p Payload) string
}
func Build(event string, p Payload, proj project.Project, cfg config.Config, r *redact.Redactor, now time.Time) (Op, bool, error)
func Handle(ctx context.Context, event string, stdin io.Reader, stdout io.Writer, d Deps) error
```

`applog`: slog JSON em `dir/mem.log`; se o arquivo passa de 5 MB ao abrir, renomeia para `mem.log.1`.

`Build` (puro): o `bool` falso significa descartar (ferramenta casa algum `SkipTools` via `path.Match`). Por evento:
- `SessionStart`: Obs `session_start`, título `session start (<source>)`, Session com `Source`.
- `UserPromptSubmit`: Obs `prompt`, body = prompt limpo, título = primeira linha até 120 caracteres.
- `PostToolUse` / `PostToolUseFailure`: Obs `tool` / `tool_error`; título `<tool_name>: <descritor>` onde descritor é o primeiro campo string presente em `tool_input` entre `file_path`, `notebook_path`, `command`, `pattern`, `url`, `description`, `query`; body = `input: <json>\noutput: <texto>` (string JSON vira texto puro; objeto vira JSON compacto) ou `error: <Error>`; `Files` = `file_path`/`notebook_path` quando `tool_name` ∈ `Edit`, `Write`, `MultiEdit`, `NotebookEdit`.
- `Stop`: Obs `stop`, título `assistant: <primeira linha>`, body = mensagem.
- `PreCompact`: Obs `compact_start`, título `compact (<trigger>)`.
- `PostCompact`: Obs `compact`, título `compact summary`, body = resumo.
- `SessionEnd`: só Session com `End=true`, `EndReason=reason`, Obs nil.
- Outro evento: erro.
Redação (`Clean`) em título e body, truncamento do body em `MaxObservationBytes`.

`Handle`: lê stdin com `io.LimitReader` de 32 MB; carrega o redator; resolve projeto pelo `cwd`; `Build`; abre o store; se abriu, drena o spool aplicando cada `Op`; aplica a `Op` (`EnsureProject`, `TouchSession`, `InsertObservation`); se abrir ou aplicar falha, `spool.Append(cfg.SpoolDir(), op)` e loga. Erro de parse loga e tenta `RecordCaptureError`. Em `SessionStart`, se `SessionStartOutput != nil`, escreve em stdout `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":<texto>}}`; texto vazio não escreve nada. `SessionEnd` roda sob `context.WithTimeout` de 1500 ms. `cmd/mem/hook.go`: `mem hook <Evento>` sempre retorna 0.

- [ ] **Step 1: Testes de `Build`** (tabela, um caso por evento) mais: `TestBuildRedactsBeforeTitle` (prompt com token → título e body sem o token), `TestBuildToolResponseObject` (`tool_response` objeto → body com JSON compacto), `TestBuildHugeOutputTruncated` (5 MB → body ≤ 4096 + sufixo), `TestBuildInvalidUTF8` (bytes `\xff` → `utf8.ValidString(body)`), `TestBuildSkipsOwnTools` (`mcp__plugin_mem-engine_mem-engine__search` → `false`), `TestBuildEditRecordsFiles`.
- [ ] **Step 2: Testes de `Handle`** com `MEM_ENGINE_HOME=t.TempDir()`: `TestHandleWritesObservation`; `TestHandleMalformedJSON` (retorna sem pânico, `CaptureErrorsSince` == 1, linha em `mem.log`); `TestHandleSpoolsWhenBusy` (outra conexão segura `BEGIN IMMEDIATE`; `StoreOptions{BusyTimeout: 50ms}` → `spool.Pending == 1`, nada no banco; libera; segundo `Handle` → 2 observações, spool vazio); `TestHandleSpoolIsRedacted` (spool não contém o token); `TestHandleSessionStartOutput` (função fake devolve `"hi"` → stdout é o JSON acima com `additionalContext == "hi"`); `TestHookCommandAlwaysZero` em `cmd/mem` com stdin inválido.
- [ ] **Step 3:** Rodar, falham.
- [ ] **Step 4:** Implementar.
- [ ] **Step 5:** `go test -race ./internal/capture/ ./cmd/mem/` passa.
- [ ] **Step 6:** `git commit -m "feat(capture): mem hook grava eventos do Claude Code com spool de fallback"`

### Task 8: Retenção e `mem gc`

**Files:**
- Create: `internal/store/gc.go`, `internal/store/gc_test.go`, `cmd/mem/gc.go`
- Modify: `internal/capture/handle.go` (SessionStart chama GC), `cmd/mem/main.go`

**Interfaces:**
- Produces: `func (s *Store) GC(ctx context.Context, now time.Time, retention time.Duration, force bool) (deleted int64, ran bool, err error)`. Sem `force`, só roda se `meta.last_gc` tiver mais de 24 h; apaga observações e `capture_errors` com `created_at < now-retention` e grava `last_gc`. `mem gc` roda com `force=true` e imprime `deleted N observations`.

- [ ] **Step 1: Testes** `TestGCDeletesExpired` (observações de 100 e 10 dias, retenção 90 → sobra 1, FTS sem a apagada), `TestGCOncePerDay` (segunda chamada sem force em 1 h → `ran == false`), `TestGCForce`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar e chamar `GC(..., false)` no `SessionStart` de `Handle` depois de gravar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(store): retenção de observações e mem gc"`

### Task 9: `mem setup` dos hooks

**Files:**
- Create: `internal/setup/hooks.go`, `internal/setup/settings.go`, `internal/setup/setup_test.go`, `cmd/mem/setup.go`
- Modify: `cmd/mem/main.go`

**Interfaces:**
- Produces:

```go
type HookEntry struct { Event, Matcher string; Async bool; Timeout int }
func Hooks() []HookEntry
func HooksConfig() map[string]any
func MergeHooks(settings []byte) (updated []byte, changed bool, err error)
func HasMemHooks(settings []byte) bool
```

`Hooks()` é a fonte única (o plugin da Task 19 é testado contra ela): `SessionStart` (síncrono, timeout 10), `UserPromptSubmit`, `PostToolUse` (matcher `*`), `PostToolUseFailure` (matcher `*`), `Stop`, `PreCompact`, `PostCompact` (todos `async`), `SessionEnd` (síncrono, timeout 2). Comando: `mem hook <Event>`. `HooksConfig()` devolve o objeto `hooks` no formato do Claude Code (`{"<Event>":[{"matcher":...,"hooks":[{"type":"command","command":...,"async":true,"timeout":N}]}]}`, omitindo campos vazios). `MergeHooks` preserva chaves e hooks de terceiros, remove entradas cujo `command` começa com `mem hook ` antes de inserir as novas (idempotente), saída indentada com 2 espaços. `mem setup --plan` imprime o caminho `~/.claude/settings.json` e o JSON das entradas que seriam adicionadas; `mem setup` pede `y/N` em stdin, grava backup `settings.json.bak` e aplica; `--yes` pula a pergunta.

- [ ] **Step 1: Testes** `TestMergeIntoEmpty` (`{}` → 8 eventos, `async` só nos de captura), `TestMergePreservesOtherHooks` (hook de terceiro em `PostToolUse` continua lá), `TestMergeIdempotent` (segunda chamada → `changed == false`, bytes iguais), `TestMergeReplacesOldMemHooks`, `TestSetupPlanWritesNothing` (em `cmd/mem` com `HOME` temporário), `TestSetupYesWritesBackup`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(setup): registro dos hooks no settings do Claude Code"`

### Task 10: `mem doctor`

**Files:**
- Create: `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`, `cmd/mem/doctor.go`
- Modify: `cmd/mem/main.go`, `README.md` (instalação com `go install`, `mem setup`, `mem doctor`, configuração)

**Interfaces:**
- Produces:

```go
type Level int
const ( OK Level = iota; Warn; Fail )
type Check struct { Name string; Level Level; Detail string }
type Env struct {
	Cfg config.Config; UserHome string
	LookPath func(string) (string, error); Executable string; Now time.Time
}
func Run(ctx context.Context, env Env) []Check
```

Checagens desta task: `binary` (`mem` no PATH e igual a `Executable` após `EvalSymlinks`, senão Warn), `config` (parse), `data-dir` (gravável), `database` (abre, versão, FTS5; `ErrSchemaTooNew` → Fail com "upgrade mem"), `spool` (`Pending > 0` → Warn com contagem), `capture-errors` (últimas 24 h > 0 → Warn com a última mensagem), `hooks` (`HasMemHooks` em `~/.claude/settings.json`, senão Warn "run mem setup"). Saída: uma linha por check, `ok|warn|FAIL  <name>  <detail>`; exit 1 se houver Fail.

- [ ] **Step 1: Testes** `TestDoctorHealthy` (home e dados temporários com hooks aplicados → nenhum Fail nem Warn exceto `binary`), `TestDoctorSpoolPending`, `TestDoctorSchemaTooNew`, `TestDoctorMissingHooks`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam; `go build ./cmd/mem` e rodar `MEM_ENGINE_HOME=$(mktemp -d) ./mem doctor` mostra a lista.
- [ ] **Step 5:** `git commit -m "feat(doctor): checagem de binário, banco, spool, erros e hooks"`

**Checkpoint da fase 1:** `go test -race ./...` verde; num diretório temporário, `echo '{"session_id":"s1","cwd":"'$PWD'","hook_event_name":"UserPromptSubmit","prompt":"hello"}' | MEM_ENGINE_HOME=$tmp ./mem hook UserPromptSubmit` e `sqlite3 $tmp/mem.db 'select kind,title from observations'` mostra `prompt|hello`.

---

## Fase 2: memória curada e MCP `search`/`timeline`/`get`/`save`

Entrega: o agente busca o que foi capturado e grava memória curada em Markdown versionado.

### Task 11: Wiki em Markdown com git

**Files:**
- Create: `internal/wiki/doc.go`, `internal/wiki/repo.go`, `internal/wiki/wiki_test.go`

**Interfaces:**
- Produces:

```go
type Doc struct {
	ID, Type, Title, Summary, Project string; Tags []string
	Created, Updated time.Time; SourceSession string; Body string
}
func Marshal(d Doc) []byte
func Parse(b []byte) (Doc, error)
type Repo struct { Dir string }
func OpenRepo(wikiRoot, dirName string) (*Repo, error)
func (r *Repo) Lock(ctx context.Context) (unlock func(), err error)
func (r *Repo) UniqueName(slug string) string
func (r *Repo) Write(name string, data []byte) error
func (r *Repo) Remove(name string) error
func (r *Repo) Commit(msg string, paths ...string) error
func (r *Repo) List() ([]string, error)
```

Frontmatter YAML entre `---` com as chaves `id, type, title, summary, project, tags, created, updated, source_session` (datas RFC 3339), corpo depois. `Parse` exige `id`, `type`, `title`. `Lock` usa `gofrs/flock` em `<Dir>/.mem.lock` com `TryLockContext` e retry de 50 ms. `UniqueName("billing-retry")` → `billing-retry.md`, `billing-retry-2.md`, ... `Write` grava em temporário no mesmo diretório e renomeia. `Commit`: `git init` se não houver `.git` (criando `.gitignore` com `.mem.lock`); `git add -- paths`; `git -c user.name=mem-engine -c user.email=mem-engine@localhost -c commit.gpgsign=false commit -m msg -- paths`. Remoção commita com `git rm --cached`/pathspec do arquivo apagado. `List` devolve os `*.md` do diretório.

- [ ] **Step 1: Testes** `TestMarshalParseRoundTrip` (acentos e `[[link]]` no corpo), `TestParseRejectsMissingID`, `TestUniqueName`, `TestWriteCommitCreatesRepo` (`git log --oneline` com 1 commit e só o arquivo), `TestCommitOnlyOwnFiles` (arquivo estranho no diretório fica fora do commit), `TestRemoveCommits`, `TestLockSerializes` (duas goroutines, a segunda só entra após `unlock`).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(wiki): memória em Markdown com frontmatter, lock e commit por operação"`

### Task 12: Serviço de memória e `mem reindex`

**Files:**
- Create: `internal/store/memories.go`, `internal/store/memories_test.go`, `internal/memory/service.go`, `internal/memory/service_test.go`, `cmd/mem/reindex.go`
- Modify: `cmd/mem/main.go`

**Interfaces:**
- Produces (store):

```go
type Memory struct {
	ID, Project, Type, Title, Summary, Body string; Tags []string
	Path, SourceSession string; Created, Updated time.Time
}
func (s *Store) UpsertMemory(ctx context.Context, m Memory) error
func (s *Store) DeleteMemory(ctx context.Context, id string) error
func (s *Store) GetMemory(ctx context.Context, id string) (Memory, error)
func (s *Store) ReplaceAllMemories(ctx context.Context, ms []Memory) error
func (s *Store) ProjectByKey(ctx context.Context, key string) (ProjectRow, error)
func (s *Store) FindProject(ctx context.Context, nameOrKey string) (ProjectRow, error)
var ErrNotFound = errors.New("not found")
```

- Produces (memory):

```go
type Input struct { Type, Title, Summary, Body string; Tags []string; SourceSession string }
type Patch struct { Type, Title, Summary, Body *string; Tags *[]string }
type ReindexReport struct { Indexed int; Skipped []string }
type Service struct { Store *store.Store; WikiRoot string; Now func() time.Time }
func (s *Service) Save(ctx context.Context, p project.Project, in Input) (store.Memory, error)
func (s *Service) Update(ctx context.Context, id string, patch Patch) (store.Memory, error)
func (s *Service) Forget(ctx context.Context, id string) error
func (s *Service) Reindex(ctx context.Context) (ReindexReport, error)
```

Id: `m-` + 12 hex de `crypto/rand`. `Type`: livre, validado por `^[a-z][a-z0-9-]{0,31}$` (recomendados: `fact`, `decision`, `gotcha`, `preference`, `reference`); `Title` e `Summary` obrigatórios. Nome do arquivo: `UniqueName(project.Slug(title))`, fixo depois de criado. Fixar memória no handoff = tag `pinned`. `Path` no índice é relativo ao `WikiRoot`. `Save`: `EnsureProject`, lock, escreve, commit `mem: save <id> <title>`, upsert. `Update` e `Forget` acham o arquivo pelo `Path` do índice, regravam ou apagam, commitam e atualizam o índice. `Reindex` percorre todos os diretórios do wiki, faz `Parse`, junta em `ReplaceAllMemories`; arquivo que falha entra em `Skipped` com o caminho e o erro. `mem reindex` imprime `indexed N, skipped M` e cada caminho pulado.

- [ ] **Step 1: Testes** `TestSaveWritesFileCommitAndIndex`, `TestSaveValidatesType`, `TestUpdateChangesFileAndIndex` (título novo, mesmo arquivo), `TestForgetRemovesFileAndIndex`, `TestReindexRebuildsFromWiki` (apagar `mem.db`, reabrir, reindex → mesmas memórias), `TestReindexSkipsBrokenFrontmatter` (um arquivo com `---\nid: [\n---` → `Indexed == 2`, `Skipped` contém o caminho), `TestUserScope` (`Save(project.User, ...)` grava em `wiki/user/`).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(memory): save, update, forget e reindex a partir do wiki"`

### Task 13: Busca, timeline e get no store

**Files:**
- Create: `internal/store/search.go`, `internal/store/search_test.go`

**Interfaces:**
- Produces:

```go
type Scope struct { Projects []string }
func CurrentScope(projectKey string) Scope
type Hit struct {
	ID, Kind, Type, Title, Summary, Project, SessionID string
	At time.Time; Score float64
}
type Item struct { Hit; Body string; Tags []string; ToolName string; Files []string; Truncated bool }
type SearchParams struct { Query, Type string; Since time.Time; Limit int; Scope Scope }
func (s *Store) Search(ctx context.Context, p SearchParams) ([]Hit, error)
func (s *Store) Timeline(ctx context.Context, id string, before, after int, sc Scope) ([]Hit, error)
func (s *Store) Get(ctx context.Context, ids []string, sc Scope, maxBytes int) ([]Item, error)
func FTSQuery(q string) string
```

Ids públicos: memória `m-...`, observação `o-<rowid>`. `Kind` ∈ `memory`, `observation`; `Type` = tipo da memória ou `kind` da observação. `CurrentScope(k)` = `{k, "user"}`; observações nunca têm projeto `user`. `FTSQuery`: extrai tokens `[\p{L}\p{N}_]+`, envolve cada um em aspas duplas, junta com ` OR `; sem tokens → `""`. Busca com `FTSQuery == ""` lista por data desc. Ranking: busca até `3*Limit` em cada FTS, `score = -bm25 * peso * recência`, `peso` 2.0 memória e 1.0 observação, `recência = 1/(1+idadeDias/30)`, ordena desc, corta em `Limit` (padrão 10, máximo 50). `Summary` de observação = primeira linha do body até 100 caracteres. `Timeline`: para observação, vizinhas na mesma sessão por `created_at`, `before` antes e `after` depois, incluindo o item; para memória com `source_session`, observações dessa sessão em volta de `created`; sem sessão, só o item. Id fora do `Scope` → `ErrNotFound`, tanto em `Timeline` quanto em `Get` (que omite o item e segue). `Get` corta bodies para que a soma não passe `maxBytes`, marcando `Truncated`.

- [ ] **Step 1: Testes**
  - `TestSearchRanksMemoryAboveObservation` (mesmo termo, memória primeiro).
  - `TestSearchRecencyBreaksTie`.
  - `TestSearchScopeIsolation`: dados nos projetos `/p/a` e `/p/b` e memória `user`; busca com `CurrentScope("/p/a")` nunca traz `/p/b` e traz a memória `user`.
  - `TestSearchDiacritics`: "nao" acha "não".
  - `TestFTSQuerySpecialChars`: para cada uma de `"a \"b"`, `a-b`, `foo:bar`, `x*`, `NEAR(a b)`, `AND`, `""`, `)(`, `Search` não retorna erro.
  - `TestTimelineSameSession` (10 observações, `before=2, after=2` no 5º → 5 itens em ordem).
  - `TestGetOtherProjectHidden` e `TestTimelineOtherProjectNotFound`.
  - `TestGetRespectsMaxBytes`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(store): busca em camadas com BM25, recência e isolamento por projeto"`

### Task 14: Servidor MCP `mem mcp`

**Files:**
- Create: `internal/mcpserver/server.go`, `internal/mcpserver/format.go`, `internal/mcpserver/server_test.go`, `cmd/mem/mcp.go`
- Modify: `cmd/mem/main.go`, `internal/setup/settings.go` (MCP), `cmd/mem/setup.go`, `internal/doctor/doctor.go`, `README.md`

**Interfaces:**
- Consumes: `store.Search/Timeline/Get`, `memory.Service`, `project.Resolve`.
- Produces:

```go
type Deps struct { Store *store.Store; Memory *memory.Service; Current project.Project; Now func() time.Time }
func New(d Deps) *mcp.Server
func MCPAddArgs() []string
```

Projeto atual: `$CLAUDE_PROJECT_DIR` se definido, senão `cwd`, via `project.Resolve`. Ferramentas (registradas com `mcp.AddTool`, `Out` = `any`, resposta em `TextContent` curto, sem saída estruturada para não duplicar tokens):
- `search{query, type?, since?, project?, limit?}`: descrição começa com "Step 1 of 3" e manda seguir com `timeline` e `get` só dos ids escolhidos. `since` aceita `YYYY-MM-DD` ou `<N>d`. Linha por item: `<id> <YYYY-MM-DD> <type> <title> | <summary>`.
- `timeline{id, before?=5, after?=5, project?}`: "Step 2 of 3", mesmo formato, item central marcado com `>`.
- `get{ids[], project?}`: "Step 3 of 3", limite total 20000 bytes, cabeçalho por item e body.
- `save{type, title, summary, body, tags?, scope?}`: `scope` ∈ `project` (padrão) e `user`; descrição diz para usar no lugar de escrever arquivos de memória e lista os tipos recomendados. Responde com o id.
- `update{id, type?, title?, summary?, body?, tags?}` e `forget{id}`.
`project` em `search`/`timeline`/`get`: descrição diz para omitir salvo pedido explícito do usuário; valor é nome ou chave, resolvido com `FindProject`; desconhecido → erro de ferramenta. Com `project`, `Scope = {chave, "user"}`.

`MCPAddArgs()` = `["mcp","add","--scope","user","mem-engine","--","mem","mcp"]`. `mem setup --plan` passa a mostrar `claude <args>`; `mem setup` executa via `exec.Command("claude", ...)` (runner injetável no teste). `doctor` ganha a checagem `mcp` (Warn se `claude mcp get mem-engine` falha; pulada se `claude` não está no PATH).

- [ ] **Step 1: Testes** com `mcp.NewInMemoryTransports` e um cliente `mcp.NewClient`: `TestListToolsOrderHints` (6 ferramentas; descrições de search/timeline/get contêm "Step 1 of 3", "Step 2 of 3", "Step 3 of 3"); `TestSearchTimelineGetFlow` (salva via `save`, acha via `search`, abre via `get`); `TestSearchDefaultsToCurrentProject` (dado de outro projeto não aparece); `TestSearchExplicitProject` (com `project: "<nome>"` aparece); `TestGetOtherProjectWithoutParam` (responde sem o body); `TestSaveUserScope`; `TestSearchLineBudget` (10 itens ocupam ≤ 10*60*4 bytes).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar; `cmd/mem/mcp.go` roda `server.Run(ctx, &mcp.StdioTransport{})`.
- [ ] **Step 4:** Passam; teste manual: `claude mcp add --scope user mem-engine -- ./mem mcp` num diretório de teste e `/mcp` lista as 6 ferramentas.
- [ ] **Step 5:** `git commit -m "feat(mcp): ferramentas search, timeline, get, save, update e forget"`

---

## Fase 3: handoff no SessionStart e `mem bench`

Entrega: a sessão abre com até ~300 tokens dizendo onde parou e o que existe; `mem bench` prova a economia contra o `MEMORY.md`.

### Task 15: Handoff

**Files:**
- Create: `internal/store/sessions.go`, `internal/store/sessions_test.go`, `internal/handoff/handoff.go`, `internal/handoff/handoff_test.go`
- Modify: `cmd/mem/hook.go` (preenche `Deps.SessionStartOutput`)

**Interfaces:**
- Produces:

```go
type SessionSummary struct { SessionID string; StartedAt time.Time; LastPrompt, LastAssistant string; Files []string }
func (s *Store) LastSession(ctx context.Context, project, excludeSession string) (SessionSummary, bool, error)
func (s *Store) LatestCompactSummary(ctx context.Context, sessionID string) (string, bool, error)
func (s *Store) RecentMemories(ctx context.Context, sc Scope, limit int) ([]Memory, error)

const MaxChars = 1200
type Input struct { Project store.ProjectRow; Source string; Last *store.SessionSummary; Compact string; Memories []store.Memory }
func Build(in Input) string
func Collect(ctx context.Context, st *store.Store, p store.ProjectRow, sessionID, source string) (Input, error)
```

`LastSession`: sessão mais recente do projeto com ao menos uma observação, excluindo a atual; último `prompt`, última mensagem `stop`, até 5 arquivos distintos mais recentes de observações `tool` com `files`. `RecentMemories`: as com tag `pinned` primeiro, depois por `updated` desc. Texto de `Build`:

```
mem-engine | project billing-api
Last session 2026-11-20 14:02: asked "<prompt, 120 chars>"; edited a.go, b.go; stopped at "<assistant, 160 chars>"
Memories: m-1a2b3c Retry policy for payments; m-...; (max 5)
Lookup: search -> timeline -> get (memory tools of this project).
```

Em `Source == "compact"` com resumo: a linha de sessão vira `Compact summary: <resumo truncado>`. Sem sessão anterior e sem memórias: só a primeira e a última linha. Resultado cortado em `MaxChars` em fronteira de rune.

- [ ] **Step 1: Testes** `TestBuildFullUnderBudget` (sessão longa e 5 memórias com títulos de 200 caracteres → `len ≤ 1200` e `len/4 ≤ 300`), `TestBuildEmptyProject`, `TestBuildCompactUsesSummary`, `TestCollectExcludesCurrentSession`, `TestRecentMemoriesPinnedFirst`, `TestHookSessionStartInjectsHandoff` (em `cmd/mem`: grava sessão s1, roda `mem hook SessionStart` para s2 → stdout JSON com `additionalContext` contendo o prompt de s1).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(handoff): resumo curto da última sessão no SessionStart"`

### Task 16: `mem bench`

**Files:**
- Create: `internal/bench/bench.go`, `internal/bench/bench_test.go`, `cmd/mem/bench.go`
- Modify: `cmd/mem/main.go`, `README.md`

**Interfaces:**
- Produces:

```go
type Result struct { NativePath string; NativeBytes, HandoffBytes int }
func NativeMemoryPath(userHome, projectRoot string) string
func NativeLoadedBytes(content []byte) int
func (r Result) Tokens() (native, handoff int)
func Run(ctx context.Context, st *store.Store, p project.Project, nativePath string) (Result, error)
```

`NativeMemoryPath`: `<home>/.claude/projects/<enc>/memory/MEMORY.md`, `enc` = raiz com todo caractere fora de `[A-Za-z0-9-]` trocado por `-`. `NativeLoadedBytes`: primeiras 200 linhas, no máximo 25600 bytes. `mem bench [path] [--native file]`: projeto pelo `path` (padrão `cwd`); imprime as três linhas `native MEMORY.md: N bytes loaded (~T tokens)`, `mem-engine handoff: N bytes (~T tokens)`, `saving: ~T tokens per session (P%)`; sem `MEMORY.md`, diz que não achou e mostra só o handoff.

- [ ] **Step 1: Testes** `TestNativeMemoryPath` (`/home/dev/src/github.com/acme/billing-api` → `-home-dev-src-github-com-acme-billing-api`), `TestNativeLoadedBytesCaps` (300 linhas → 200; 40 KB numa linha → 25600), `TestRunComparesSizes` (fixture gerada no teste, nunca arquivo real).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(bench): compara MEMORY.md nativo com o handoff"`

---

## Fase 4: visualizador `mem ui`

Entrega: feed ao vivo, busca, página de item com linha do tempo, edição e exclusão no navegador.

### Task 17: Servidor HTTP e API

**Files:**
- Create: `internal/store/feed.go`, `internal/store/feed_test.go`, `internal/ui/server.go`, `internal/ui/api.go`, `internal/ui/server_test.go`

**Interfaces:**
- Produces:

```go
func (s *Store) Feed(ctx context.Context, afterID int64, projects []string, limit int) ([]Hit, error)
func (s *Store) DeleteObservation(ctx context.Context, id int64) error
func (s *Store) ListProjects(ctx context.Context) ([]ProjectRow, error)

type Server struct { Store *store.Store; Memory *memory.Service; Cfg config.Config; Token string; Port int }
func NewToken() string
func (s *Server) Handler() http.Handler
```

Segurança em todo request: `Host` ∈ {`127.0.0.1:<port>`, `localhost:<port>`} senão 403; token em `?token=` ou header `X-Mem-Token`, comparado com `subtle.ConstantTimeCompare`, senão 401; métodos que mudam estado exigem `Origin` igual a `http://127.0.0.1:<port>` ou `http://localhost:<port>`, senão 403. Rotas JSON:
- `GET /api/projects`
- `GET /api/feed?after=<n>&project=<key>` (sem `project`: todos, porque é a tela do próprio usuário)
- `GET /api/search?q=&project=&type=&since=`
- `GET /api/item/<id>?project=<key>` → item completo mais `timeline`
- `PUT /api/memory/<id>` (corpo `memory.Patch` em JSON) → `Memory.Update`
- `DELETE /api/memory/<id>` → `Memory.Forget`; `DELETE /api/observation/<id>` → `DeleteObservation`
- `GET /api/health` → `{"spool_pending":N,"capture_errors_24h":N,"last_error":"..."}`
`DeleteObservation` remove também da FTS (via trigger).

- [ ] **Step 1: Testes** (`httptest`) `TestRejectsBadHost` (`Host: evil.example:47600` → 403), `TestRejectsMissingToken` (401), `TestRejectsCrossOriginWrite` (`Origin: http://evil.example` em DELETE → 403), `TestFeedAfterID`, `TestUpdateMemoryCommits` (PUT altera título; `git log` do wiki ganha commit), `TestDeleteObservation`, `TestHealth`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(ui): API local com token e checagem de Host e Origin"`

### Task 18: Front-end embutido e `mem ui`

**Files:**
- Create: `internal/ui/assets/index.html`, `internal/ui/assets/app.js`, `internal/ui/assets/app.css`, `internal/ui/assets.go`, `cmd/mem/ui.go`
- Modify: `internal/ui/server_test.go`, `cmd/mem/main.go`, `README.md`

**Interfaces:**
- Consumes: rotas da Task 17.
- Produces: `GET /` e `GET /assets/*` servidos de `embed.FS` (mesma checagem de Host e token em `/`; assets estáticos só com Host). `mem ui [--port N]` escuta em `127.0.0.1`, imprime `mem ui: http://127.0.0.1:<port>/?token=<token>` e roda até SIGINT.

Telas em JS puro, sem framework, sem CDN: barra com filtro de projeto, tipo e data e campo de busca; feed com polling de 2 s em `/api/feed?after=`; lista de resultados; painel de item com body, linha do tempo clicável, formulário de edição de memória (tipo, título, resumo, tags, body) e botões de excluir com `confirm` do navegador; selo de saúde no topo que fica vermelho quando `spool_pending > 0` ou `capture_errors_24h > 0`. O JS lê o token da URL e manda em `X-Mem-Token`.

- [ ] **Step 1: Testes** `TestIndexServedWithToken` (200 e contém `id="feed"`), `TestIndexWithoutToken401`, `TestAssetsEmbedded` (`/assets/app.js` 200, `Content-Type` JavaScript).
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Testes passam; verificação real com a skill `run`: subir `mem ui` com dados de teste, abrir a URL, confirmar feed atualizando após um `mem hook UserPromptSubmit`, busca, edição de memória gerando commit e selo de saúde vermelho com spool pendente.
- [ ] **Step 5:** `git commit -m "feat(ui): visualizador web local com feed, busca e edição"`

---

## Fase 5: plugin, marketplace e releases

Entrega: instalação por `claude plugin marketplace add` e binários publicados.

### Task 19: Plugin e marketplace

**Files:**
- Create: `.claude-plugin/marketplace.json`, `plugins/mem-engine/.claude-plugin/plugin.json`, `plugins/mem-engine/hooks/hooks.json`, `plugins/mem-engine/.mcp.json`, `plugins/mem-engine/skills/mem-engine/SKILL.md`, `internal/setup/plugin_test.go`
- Modify: `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`, `README.md`

**Interfaces:**
- Consumes: `setup.HooksConfig()`.
- Produces: `hooks.json` = `{"hooks": HooksConfig()}`; `.mcp.json` = `{"mcpServers":{"mem-engine":{"command":"mem","args":["mcp"]}}}`; `marketplace.json` com o plugin `mem-engine` em `./plugins/mem-engine`; `plugin.json` com nome, versão, descrição, licença MIT. Nada de estado em `${CLAUDE_PLUGIN_ROOT}`. A skill tem `description` dizendo para usar em qualquer pedido de lembrar, salvar ou consultar memória, e o corpo, curto, manda: buscar com `search` → `timeline` → `get`; gravar com `save` em vez de escrever arquivos `.md` de memória; `scope: user` só para preferência pessoal; nunca passar `project` sem pedido explícito.

Doctor ganha `hooks-duplicated`: Warn quando `HasMemHooks(settings.json)` e `enabledPlugins` tem chave começando com `mem-engine@`, sugerindo remover um dos dois.

- [ ] **Step 1: Testes** `TestPluginHooksMatchSpec` (`hooks.json` do repo é igual, após decode, a `{"hooks": HooksConfig()}`), `TestPluginMCPCommand`, `TestDoctorDuplicatedHooks`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Criar os arquivos.
- [ ] **Step 4:** Testes passam e `claude plugin validate .` e `claude plugin validate plugins/mem-engine` sem erro.
- [ ] **Step 5:** `git commit -m "feat(plugin): plugin do Claude Code e marketplace no próprio repositório"`

### Task 20: Releases

**Files:**
- Create: `.goreleaser.yaml`, `.github/workflows/release.yml`
- Modify: `README.md` (instalação por release, por `go install` e por plugin)

**Interfaces:**
- Produces: tag `v*` gera binários `mem` para `darwin`, `linux`, `windows` × `amd64`, `arm64`, `CGO_ENABLED=0`, `-ldflags "-s -w -X main.version={{.Version}}"`, arquivos `tar.gz` (`zip` no Windows) e `checksums.txt`.

- [ ] **Step 1:** `goreleaser check` passa.
- [ ] **Step 2:** `goreleaser release --snapshot --clean` gera os 6 binários em `dist/`; `dist/*darwin_arm64*/mem version` imprime a versão do snapshot.
- [ ] **Step 3:** `git commit -m "ci: releases multiplataforma com goreleaser"`

---

## Fase 6: importador e migração de projeto

Entrega: troca da memória nativa sem perder o que já existe.

### Task 21: `mem import`

**Files:**
- Create: `internal/importer/importer.go`, `internal/importer/importer_test.go`, `internal/importer/testdata/native/` (fixtures neutras), `cmd/mem/import.go`
- Modify: `cmd/mem/main.go`, `README.md`

**Interfaces:**
- Consumes: `memory.Service.Save/Update`, `project.Resolve`, `bench.NativeMemoryPath`.
- Produces:

```go
type Options struct { Dir string; Target project.Project; DryRun bool }
type Report struct { Imported, Skipped int; Unresolved []string; Notes []string }
func Run(ctx context.Context, svc *memory.Service, st *store.Store, o Options) (Report, error)
```

Lê todo `*.md` de `Dir` exceto `MEMORY.md`. Frontmatter nativo: `name`, `description`, tipo em `metadata.type` ou `type` no topo. Mapeia: título = `name` com `-` virando espaço; `summary` = `description`; `type` = tipo nativo preservado; tags `["imported"]`; corpo intacto. Primeira passada salva tudo e monta `name → id`; segunda passada troca `[[name]]` por `[[<id>]]` quando resolve (via `Update`) e lista em `Unresolved` os que ficam como texto. Já existe memória do alvo com tag `imported` e mesmo título → pula e conta em `Skipped`. Nunca altera nem apaga `Dir`. CLI: `mem import <dir> [--project <path-or-name>] [--user] [--dry-run]`; sem `--project` nem `--user`, usa `user` se `Dir` for o diretório de memória nativa do próprio home (`filepath.Dir(NativeMemoryPath(home, home))`), senão erro pedindo `--project`.

- [ ] **Step 1: Testes** com fixtures `testdata/native/` (`retry-policy.md` tipo `project` que linka `[[deploy-window]]`, `deploy-window.md`, `editor-preference.md` tipo `user` com `type` no topo, `broken-link.md` com `[[missing]]`, `MEMORY.md`): `TestImportPreservesTypeAndDescription`, `TestImportRewritesLinks`, `TestImportReportsUnresolved`, `TestImportSkipsMemoryIndex`, `TestImportIsRerunnable` (segunda execução → `Imported == 0`, `Skipped == 4`), `TestImportDoesNotTouchSource` (hash do diretório igual antes e depois), `TestImportDryRunWritesNothing`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** Passam.
- [ ] **Step 5:** `git commit -m "feat(import): importação da memória nativa do Claude Code"`

### Task 22: `mem project merge|alias` e desligar a memória nativa

**Files:**
- Create: `internal/store/merge.go`, `internal/store/merge_test.go`, `internal/memory/move.go`, `internal/memory/move_test.go`, `cmd/mem/project.go`
- Modify: `internal/setup/settings.go`, `internal/setup/setup_test.go`, `cmd/mem/setup.go`, `cmd/mem/main.go`, `README.md`

**Interfaces:**
- Produces:

```go
func (s *Store) MergeProject(ctx context.Context, from, to string) (moved int64, err error)
func (s *Service) MoveProject(ctx context.Context, from, to project.Project) (moved int, err error)
func DisableNativeMemory(settings []byte) (updated []byte, changed bool, err error)
```

`MergeProject` move `observations.project` e `sessions.project` numa transação. `MoveProject` regrava cada arquivo de `from` no diretório de `to` com `project` novo (mesmo id, nome único no destino), commita nos dois repos, atualiza o índice. `mem project merge <from> <to>` (nome ou chave) chama os dois e imprime as contagens. `mem project alias <old-key> <new-path>` anexa ao `config.toml` um bloco `[[alias]]` com `path = "<new-path>"` e `key = "<old-key>"`. `DisableNativeMemory` põe `autoMemoryEnabled: false` preservando o resto; `mem setup --disable-native-memory` mostra e aplica com a mesma confirmação e backup da Task 9.

- [ ] **Step 1: Testes** `TestMergeProjectMovesObservations`, `TestMoveProjectMovesFilesAndCommits` (arquivo some de `from`, aparece em `to`, frontmatter com `project` novo, um commit em cada repo), `TestAliasAppendsAndResolves` (depois do alias, `project.Resolve(novo)` devolve a chave antiga), `TestDisableNativeMemoryIdempotent`.
- [ ] **Step 2:** Falham.
- [ ] **Step 3:** Implementar.
- [ ] **Step 4:** `go test -race ./...` verde.
- [ ] **Step 5:** `git commit -m "feat(project): merge e alias de projeto e desligamento da memória nativa"`

**Checkpoint final:** `go test -race ./...`, `go vet ./...`, `claude plugin validate .` e `goreleaser check` verdes; `mem doctor` sem Fail num ambiente com plugin instalado; `mem bench` num projeto de teste mostra handoff menor que o `MEMORY.md` de fixture.
