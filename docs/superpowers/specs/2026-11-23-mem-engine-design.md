# mem-engine: design

Nome provisório. Data: 23/11/2026.

## Intenção

Memória de longo prazo para agentes de código (Claude Code primeiro), aberta e instalável por qualquer pessoa, que gaste menos tokens do que as alternativas.

Por quê:
- O `claude-mem` foi abandonado pelo consumo de tokens: resume cada observação com LLM e injeta muito contexto na abertura da sessão.
- A memória nativa do Claude Code carrega o índice `MEMORY.md` inteiro em toda sessão. Em uso real, com centenas de memórias por projeto, isso dá de 12 a 18 KB por sessão, antes de qualquer pergunta.
- O ai-memory resolve o custo com captura sem LLM e handoff curto, mas não tem as duas coisas que mais ajudavam no `claude-mem`: a busca em camadas e o visualizador web.

Sucesso: a sessão abre com no máximo algumas centenas de tokens de memória; o agente encontra o que precisa em 2 ou 3 chamadas baratas; o usuário vê e corrige a memória num navegador.

## Objetivos

1. Busca em camadas por MCP: `search` (lista curta), `timeline` (o que houve em volta), `get` (conteúdo completo só dos ids pedidos).
2. Visualizador web local: feed do que está sendo capturado, busca com filtros, página de memória com linha do tempo, edição e exclusão.
3. Captura automática por hooks, sem nenhuma chamada a LLM.
4. Memória curada (gravada de propósito pelo agente ou pelo usuário) como Markdown num repositório git, fonte da verdade; o SQLite é índice reconstruível.
5. Handoff curto na abertura da sessão no lugar de um índice inteiro.
6. Isolamento por projeto: busca restrita ao projeto atual; outro projeto só com pedido explícito.
7. Substituir a memória nativa, com importador dos arquivos `.md` existentes.
8. Distribuição como binário único em Go e como plugin do Claude Code.

## Fora do escopo (por ora)

- Resumo por LLM a cada observação.
- Banco vetorial dedicado.
- Suporte a outros agentes além do Claude Code. O MCP já é neutro; adaptadores de hook para outros agentes ficam para depois.
- Sincronização entre máquinas ou memória compartilhada em time.

## Anti-objetivos

Falha mesmo que funcione tecnicamente:
- A abertura da sessão custar mais tokens do que o `MEMORY.md` que ela substitui.
- A captura falhar em silêncio. Hook que não grava precisa deixar rastro (log e indicador no visualizador).
- Um hook lento travar o turno do agente.
- Dado de um projeto aparecer em busca de outro sem pedido explícito.
- Mandar conteúdo capturado para qualquer serviço externo por padrão.

## Restrições

- Go, binário único, SQLite puro (`modernc.org/sqlite`, sem CGO), com FTS5.
- Sem processo residente (decisão abaixo). Vários processos podem gravar ao mesmo tempo: SQLite em modo WAL com `busy_timeout`.
- Tudo local. Visualizador só em `127.0.0.1`.
- Licença MIT.
- Código e identificadores em inglês; código sem comentários; documentação de uso no README.

## Abordagens consideradas

| | Como funciona | Troca | Decisão |
|---|---|---|---|
| A. Binário único, sem residente | Hook chama `mem hook <evento>` que grava direto no SQLite; MCP por stdio aberto pelo agente; `mem ui` sob demanda | Feed ao vivo faz polling em vez de push | **Escolhida**: nada para instalar como serviço, nada que caia em silêncio |
| B. Serviço residente (estilo `claude-mem`) | Worker numa porta local recebe eventos por HTTP | Feed instantâneo, um único escritor; mas serviço para monitorar, e queda dele perde captura sem aviso | Rejeitada |
| C. Híbrido | Usa o serviço quando está de pé, senão grava direto | Dois caminhos de código para manter | Rejeitada |

## Desenho

### Projeto

Padrão: um repositório git por projeto, identificado pela raiz comum do git do `cwd` do evento (worktrees incluídos); fora de git, pelo próprio `cwd`. A chave é o caminho absoluto normalizado.

Agrupamento por prefixo de caminho, configurável: todo `cwd` sob um prefixo vira o mesmo projeto, com nome dado pelo usuário. O prefixo mais longo vence. Exemplo de uso real : um grupo por cliente, como `src/github.com/<cliente>/` → projeto `<cliente>`, para que os repositórios do mesmo cliente compartilhem memória e o isolamento seja entre clientes.

```toml
[[group]]
name = "cliente-a"
prefix = "~/src/github.com/cliente-a/"
```

Criar ou mudar um grupo depois de já haver dados é resolvido com `mem project merge <origem> <destino>`, que move observações e memórias.

### Dados

Dois tipos, com destinos diferentes:

1. **Observação** (captura automática): prompt do usuário, chamada de ferramenta com entrada e saída truncadas, início e fim de sessão. Vai só para o SQLite. Volume alto, descartável, com retenção configurável (padrão: 90 dias).
2. **Memória** (curada): fato, decisão, pegadinha, preferência, referência. Arquivo Markdown com frontmatter (`id`, `type`, `title`, `summary`, `project`, `tags`, `created`, `updated`, `source_session`) num diretório git. O SQLite indexa e pode ser reconstruído com `mem reindex`.

Diretório de dados: `~/.mem-engine/` (configurável), com `wiki/<projeto>/` (git), `mem.db`, `logs/`.

### Captura

Hooks do Claude Code (confirmados na doc oficial, `code.claude.com/docs/en/hooks.md`). Todos recebem `session_id`, `transcript_path`, `cwd`, `hook_event_name`. Cada um chama `mem hook <evento>`, lê o JSON do stdin e grava uma linha:

| Evento | Campos usados | Modo |
|---|---|---|
| `SessionStart` | `source` (startup, resume, clear, compact, fork) | síncrono, injeta o handoff |
| `UserPromptSubmit` | `prompt` | `async: true` |
| `PostToolUse` / `PostToolUseFailure` | `tool_name`, `tool_input`, `tool_response`, `duration_ms` | `async: true` |
| `Stop` | `last_assistant_message` (a doc recomenda usar este campo em vez do transcript) | `async: true` |
| `PreCompact` / `PostCompact` | `trigger`, `compact_summary` | `async: true` |
| `SessionEnd` | `reason` | síncrono, orçamento de 1,5 s: só fecha a sessão no banco |

Regras:
- Hooks de captura usam `async: true`, então nunca bloqueiam o turno. Mesmo assim cada um termina em milissegundos; erro vai para `logs/` e para um contador que o visualizador mostra.
- Subagentes (`SubagentStop`, com `agent_id`) ficam fora da primeira versão.
- Texto entre `<private>` e `</private>` é removido antes de gravar.
- Redação de segredos por padrões configuráveis (tokens, chaves, connection strings).
- Saída de ferramenta truncada (padrão: 4 KB por observação).

### Abertura da sessão

O hook `SessionStart` devolve o handoff em `hookSpecificOutput.additionalContext` (limite de 10.000 caracteres por campo; acima disso o Claude Code troca por arquivo e prévia). Meta: até 300 tokens, bem abaixo do limite:
- uma linha sobre a última sessão do projeto: quando, o que foi pedido, onde parou;
- até 5 títulos de memórias recentes ou fixadas, com id;
- uma linha lembrando a ordem `search` → `timeline` → `get`.

O "onde parou" sai sem LLM: último prompt do usuário, últimos arquivos editados e `last_assistant_message` do último `Stop`, truncados. Em `source: compact`, o handoff inclui o `compact_summary` mais recente em vez do resumo da sessão anterior.

### Busca em camadas (MCP)

- `search(query, type?, since?, project?, limit=10)`: id, data, tipo, título, resumo de uma linha (~50 tokens por item). Ranking: BM25 do FTS5 com peso de recência; memória curada acima de observação.
- `timeline(id, before=5, after=5)`: itens vizinhos na mesma sessão, em formato curto.
- `get(ids[])`: conteúdo completo, com limite total de tamanho.
- `save(type, title, summary, body, tags?)`: grava memória curada (substitui a escrita de arquivos da memória nativa).
- `update(id, ...)` e `forget(id)`.

A descrição de cada ferramenta ensina a ordem de uso. `project` diferente do atual exige valor explícito.

### Visualizador (`mem ui`)

Servidor HTTP embutido, `127.0.0.1`, porta configurável, HTML e JS embutidos no binário, sem framework pesado.
- Feed ao vivo por polling (2 s) das observações novas.
- Busca com filtros de projeto, tipo, data.
- Página de item com linha do tempo da sessão.
- Edição e exclusão de memória curada (grava no Markdown e faz commit) e exclusão de observação.
- Indicador de falhas de captura.

### Substituição da memória nativa

- `mem import <dir>` lê os `.md` com frontmatter da memória nativa e cria memórias curadas, preservando tipo, descrição e links `[[nome]]`.
- Desligar a memória nativa com `autoMemoryEnabled: false` no settings de usuário ou de projeto (equivalente: `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`). Hoje ela carrega as primeiras 200 linhas ou 25 KB do `MEMORY.md` em toda sessão; é esse custo que sai.
- O próprio plugin traz uma skill curta que instrui o agente a usar `save` em vez de escrever arquivos.
- Importação não apaga a origem.

### Distribuição

- Releases com binário para macOS, Linux e Windows.
- Plugin do Claude Code no próprio repositório, que também é o marketplace: `.claude-plugin/marketplace.json` na raiz e o plugin em `plugins/mem-engine/` com `.claude-plugin/plugin.json`, `hooks/hooks.json`, `.mcp.json` e `skills/`. Comandos chamam o binário pelo `PATH`; o plugin não guarda estado em `${CLAUDE_PLUGIN_ROOT}`, que muda a cada versão. Instalação: `claude plugin marketplace add <owner>/<repo>` e `claude plugin install mem-engine@<marketplace>`. Validação com `claude plugin validate`.
- `mem setup --plan` mostra o que seria alterado; `mem setup` aplica com confirmação; `mem doctor` checa hooks, banco e permissões.

### Opcionais desligados por padrão

- Embeddings por provedor configurável, como sinal extra no ranking.
- Consolidação periódica de observações em memória curada por LLM.

## Decisões do autor do design (podem ser revistas)

Respostas às perguntas do revisor que não dependiam do usuário:

- **Latência e concorrência da captura:** os hooks de captura são `async: true`, então a latência não trava o turno. Cada hook grava direto com `busy_timeout` de 2 s; se estourar, anexa a linha num spool `spool/*.jsonl` que o próximo processo que abrir o banco drena. Nada se perde em silêncio: spool pendente aparece no `mem doctor` e no visualizador.
- **Escrita de memória curada:** lock de arquivo no diretório do projeto no wiki; grava o Markdown em arquivo temporário e renomeia (atômico), atualiza o índice e faz um commit por operação, só com os próprios arquivos. Em divergência, vale o arquivo; `mem reindex` reconstrói o banco a partir do wiki. Diretório que não é repo git vira repo na primeira escrita.
- **Worktrees:** a chave do projeto usa a raiz comum do git (`git rev-parse --git-common-dir`), então worktrees caem no mesmo projeto, como faz a memória nativa.
- **Diretório no wiki:** slug legível do nome do projeto mais hash curto da chave (ex.: `billing-api-3fa2c1`). Repo que mudou de lugar se resolve com alias na configuração (`mem project alias <antigo> <novo>`).
- **Memória de usuário:** além do escopo de projeto existe o escopo `user`, para preferências pessoais que valem em qualquer projeto (ex.: idioma, ferramenta de áudio). Ele aparece na busca de qualquer projeto; dado de um projeto nunca aparece em outro sem pedido explícito.
- **Importador:** `mem import <dir> --project <chave>`; o diretório de memória do home vai para o escopo `user`. Links `[[nome]]` viram referência ao id novo; os que não resolvem ficam como texto. Colisão de nome ganha sufixo.
- **Medição:** tokens estimados por bytes/4. O handoff tem corte rígido em 1.200 caracteres. `mem bench <projeto>` compara o tamanho do `MEMORY.md` nativo com o handoff gerado; o corpus é a memória local do próprio usuário e nunca entra no repositório.
- **Segurança do visualizador:** `mem ui` gera token aleatório a cada execução e imprime a URL com ele; o servidor exige o token e confere `Host` e `Origin` contra `127.0.0.1`, bloqueando DNS rebinding e CSRF. O MCP é stdio, sem porta.
- **Retenção:** a limpeza de observações vencidas roda no `SessionStart`, no máximo uma vez por dia, e também com `mem gc`. Redação de segredos tem lista mínima de fábrica (tokens de GitHub, Slack, AWS, OpenAI, Anthropic, JWT, connection strings, chaves privadas PEM), testada, que o usuário pode ampliar mas não desligar sem flag explícita.
- **Versão de esquema:** tabela `schema_version`; o binário migra ao abrir, sob lock; binário mais velho que o banco recusa gravar, registra no log e o `mem doctor` avisa. Hooks, MCP e visualizador chamam o mesmo binário do `PATH`, então não há duas versões convivendo no caso normal.

## Deixado para quem implementa

- Esquema exato das tabelas e do FTS5.
- Formato de log e de configuração (sugestão: TOML).
- Layout do visualizador.
- Pesos do ranking.
