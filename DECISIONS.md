# DECISIONS

## 23/11/2026, releases com goreleaser fixado em versão e actions fixadas por SHA

- Decisão: `.goreleaser.yaml` (version 2) gera `mem` para darwin, linux e windows em amd64 e arm64 com `CGO_ENABLED=0`, `-trimpath` e `-s -w -X main.version={{.Version}}`, arquivos `tar.gz` (`zip` no Windows) com `LICENSE` e `README.md`, e `checksums.txt` em sha256. O `release.yml` roda em tag `v*`, com `contents: write` só no job, testes antes do goreleaser, `GITHUB_TOKEN` apenas, `persist-credentials: false` e sem cache do setup-go. O goreleaser fica em `v2.20.0` no workflow e localmente roda por `go run github.com/goreleaser/goreleaser/v2@v2.20.0`; `actions/checkout`, `actions/setup-go` e `goreleaser/goreleaser-action` são referenciados por SHA de commit com a tag em comentário, em `ci.yml` também. Só entram versões publicadas há pelo menos 14 dias. `release.github` declara `andreabreu76/mem-engine` explicitamente
- Por quê: tag móvel de action ou `~> v2` do goreleaser deixa uma release nova de terceiro entrar no pipeline que publica os binários sem revisão; a janela de 14 dias dá tempo de uma versão comprometida ser retirada. Sem `release.github`, `goreleaser check` falha em clone sem remote (`scm releases: no remote configured to list refs from`)
- Alternativa rejeitada: instalar goreleaser globalmente, actions por tag de versão e seções de homebrew, scoop, docker e assinatura (fora do escopo da v1)

## 23/11/2026, handoff ignora sessão vazia, preserva a linha `Lookup` e mostra arquivo relativo ao projeto

- Decisão: `LastSession` só considera sessão com alguma observação além de `session_start` e `compact_start`. `Build` normaliza todo texto livre para uma linha, corta por runes com `…` (prompt 120, resposta 160, resumo de compactação 480, título de memória 64, arquivo 40 cortado pela esquerda) e, se ainda passar de `MaxChars`, corta o corpo em bytes na fronteira de rune antes da linha `Lookup`, que sempre fica inteira. Arquivo aparece relativo à chave do projeto e, fora dela, só pelo nome. Erro em `Collect` vai para o `mem.log` e o hook devolve só a primeira e a última linha
- Por quê: toda sessão grava `session_start`, então uma sessão aberta e fechada sem uso viraria a "última sessão" com a linha vazia; a linha `Lookup` é a instrução que leva o agente às ferramentas MCP e não pode sumir no corte final; caminho absoluto gastaria o orçamento repetindo o prefixo do projeto, e o fim do caminho é a parte útil
- Alternativa rejeitada: corte final cego em 1200 bytes (pode decepar a linha `Lookup`) e caminho absoluto ou só o nome base para arquivo dentro do projeto (ambíguo entre pacotes)

## 23/11/2026, ferramentas MCP de escrita presas ao escopo atual e `mem setup` consultando o `claude` antes de registrar

- Decisão: `update` e `forget` só agem sobre memória do projeto atual ou `user`; id de outro projeto responde "not found", igual a id inexistente. Erro volta ao agente como resultado com `isError`: os que ele pode corrigir (tipo, título ou resumo inválido, `scope`, `since`, projeto desconhecido ou ambíguo, id não encontrado, arquivo com outra memória) com o texto original, qualquer outro como `<tool> failed, see mem.log`, sempre com o erro inteiro no `mem.log`, para não mandar caminho local nem saída do git ao provedor do modelo. `mem setup` roda `claude mcp get mem-engine` antes de `claude mcp add` e pula o registro quando já existe ou quando `claude` não está no PATH; `--plan` só imprime o comando. O `claude` é chamado por `runCommand`, variável de pacote em `cmd/mem` que o `TestMain` troca por um falso, e o doctor recebe o mesmo executor em `Env.RunCommand`
- Por quê: as duas ferramentas não têm parâmetro `project`, então aceitar id de outro projeto abriria escrita fora do isolamento que vale para a leitura e colocaria caminho do wiki de outro projeto na mensagem de erro; `claude mcp add` falha quando o servidor já existe, o que tornaria um segundo `mem setup` um erro; nenhum teste pode executar o `claude` real
- Alternativa rejeitada: `update`/`forget` por id em qualquer projeto (quebra o isolamento) e `project` também nas ferramentas de escrita (o brief não pede e o agente não precisa)

## 23/11/2026, commit do wiki isolado da configuração git do usuário

- Decisão: `wiki.Repo` chama o binário `git` com `-C <Dir> --literal-pathspecs -c core.hooksPath=<Dir>/.git/mem-engine-hooks` (diretório vazio) em toda chamada, `--no-verify` no commit, identidade `mem-engine` e `commit.gpgsign=false` via `-c`, e remove `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` e afins do ambiente. O commit usa pathspec (`git add --all -- paths` e `git commit -- paths`), então só entram os arquivos da operação, inclusive a remoção
- Por quê: um `core.hooksPath` global ou `init.templateDir` com hooks poderia bloquear o commit ou ler o conteúdo da memória; um `GIT_DIR` herdado faria o commit cair no repositório do usuário; `--literal-pathspecs` evita que um nome com `[` vire glob
- Alternativa rejeitada: biblioteca git em Go (dependência grande para quatro comandos) e `--no-verify` sozinho, que não cobre `prepare-commit-msg` nem `post-commit`

## 23/11/2026, `mem setup` compara o settings.json pelo conteúdo decodificado e grava através de symlink

- Decisão: `MergeHooks` decodifica com `UseNumber`, remove as entradas cujo `command` começa com `mem hook `, insere as de `Hooks()` e só reporta mudança se o resultado decodificado difere do original; sem mudança, devolve os bytes de entrada intactos. `WriteSettings` resolve symlink antes do `rename` atômico, preserva o modo do arquivo e grava `settings.json.bak` (0600) ao lado do caminho original
- Por quê: comparar bytes faria um settings já configurado, mas com outra formatação, ser reescrito e ganhar backup a cada `mem setup`; `UseNumber` evita reescrever inteiros grandes como float; `rename` direto sobre um symlink trocaria o link de dotfiles por um arquivo comum
- Alternativa rejeitada: comparar bytes de saída, e editar o JSON preservando a ordem das chaves (exige parser próprio; a reordenação alfabética foi aceita)

## 23/11/2026, drain do spool separa erro permanente por validação local, não pelo erro do banco

- Decisão: no `apply` do `spool.Drain`, linha que não decodifica como `Op` ou que falha em `validate` (sem projeto, `kind` desconhecido) vai para `capture_errors` e `mem.log` e é descartada; qualquer erro do store depois da validação interrompe o drain e a linha fica no spool. Se o próprio `RecordCaptureError` falhar, o drain também para, para não descartar sem rastro
- Por quê: o store não expõe sentinela para erro de validação nem para SQLITE_BUSY; validar antes no `capture`, que produz os `kind`, evita depender do texto do erro e garante que só sai do spool o que deixou registro
- Alternativa rejeitada: classificar pelo código do SQLite (BUSY/LOCKED como transitório, resto permanente), que descartaria linhas válidas em disco cheio ou erro de I/O

## 23/11/2026, spool publica por temporário e `os.Rename`, com recuperação de órfãos

- Decisão: `spool.Append` grava em `.spool-*.tmp` e renomeia para `<pid>-<unixnano>.jsonl`, com `unixnano` estritamente crescente dentro do processo; `Drain` reivindica por `rename` para `.draining-<pid>`, atualiza o mtime ao reivindicar e a cada linha, e recupera `.draining-*` e `.spool-*.tmp` com mais de 1 minuto; um mutex serializa `Drain` dentro do processo
- Por quê: com `O_APPEND|O_CREATE` direto no nome final, um `Drain` de outro processo pode renomear o arquivo entre a criação e a escrita, ler vazio e apagar, perdendo a linha. Com `os.Link`, um `Append` morto entre o link e a remoção do temporário deixaria o mesmo conteúdo em dois nomes, e a recuperação de órfãos aplicaria a linha duas vezes. O `rename` mantém um único nome por conteúdo; o mtime renovado impede que um dreno vivo seja tomado como órfão (o `rename` preserva o mtime de quando a linha foi gravada)
- Alternativa rejeitada: `os.Link` (duplicidade descrita acima); `flock`, que não é portátil sem cgo; `RENAME_NOREPLACE`, que depende de syscall por plataforma

## 23/11/2026, raiz do git detectada em Go puro, sem chamar o binário `git`

- Decisão: `project.Resolve` sobe até o `.git`; se for arquivo (worktree), lê `gitdir:` e `commondir` para chegar à raiz comum
- Por quê: roda em todo hook; evitar um processo filho por evento mantém a captura em milissegundos e não depende de `git` no PATH
- Alternativa rejeitada: `git rev-parse --git-common-dir`, mesmo resultado com custo de processo e versão mínima do git

## 23/11/2026, um repositório git por projeto dentro de `wiki/`

- Decisão: cada `wiki/<slug>-<hash>/` é um repo próprio, com lock e commit por operação
- Por quê: a spec define `wiki/<projeto>/ (git)` e o lock por diretório de projeto; repos separados deixam o merge e a remoção de um projeto triviais
- Alternativa rejeitada: um único repo em `wiki/`, que serializaria escritas de projetos diferentes no mesmo índice do git

## 23/11/2026, tipo de memória livre, validado por regex

- Decisão: `type` aceita `^[a-z][a-z0-9-]{0,31}$`, com `fact`, `decision`, `gotcha`, `preference` e `reference` como recomendados
- Por quê: o importador precisa preservar o tipo da memória nativa (`user`, `feedback`, `project`, `reference`) sem tabela de conversão
- Alternativa rejeitada: enum fechado com mapeamento, que perde a informação original

## 23/11/2026, strings do produto e README em inglês

- Decisão: CLI, handoff, descrições das ferramentas MCP, visualizador e README em inglês; commits e docs de design em português
- Por quê: projeto público, instalável por qualquer pessoa, e o handoff entra no contexto do agente
- Alternativa rejeitada: tudo em português, que reduz o alcance do projeto
