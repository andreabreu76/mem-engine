# DECISIONS

## 23/11/2026, spool publica cada arquivo por temporário e `os.Link`

- Decisão: `spool.Append` grava a linha em `.spool-*.tmp` e publica com `os.Link` para `<pid>-<unixnano>.jsonl`, repetindo com novo nome se já existir; o `Drain` só enxerga `*.jsonl`
- Por quê: com `O_APPEND|O_CREATE` direto no nome final, um `Drain` de outro processo pode renomear o arquivo entre a criação e a escrita, ler vazio e apagar, perdendo a linha; o `Link` também não sobrescreve um nome repetido no mesmo nanossegundo (relógio do macOS tem resolução de microssegundos)
- Alternativa rejeitada: `os.Rename` do temporário, que sobrescreve silenciosamente um `.jsonl` de mesmo nome; `flock`, que não é portátil sem cgo

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
