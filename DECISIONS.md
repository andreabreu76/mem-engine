# DECISIONS

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
