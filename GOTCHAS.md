# GOTCHAS

## 24/11/2026, testes que trocam `HOME` não isolam o home no Windows

- Sintoma: no CI do Windows, config, bench e import leem `C:\Users\runneradmin` em vez do diretório temporário do teste
- Causa: `os.UserHomeDir` no Windows lê `USERPROFILE`, não `HOME`
- Como lidar: todo `t.Setenv("HOME", x)` vem junto de `t.Setenv("USERPROFILE", x)`; o `.gitattributes` com `eol=lf` evita a outra metade das falhas (golden e frontmatter com CRLF no checkout)
- Onde: `internal/config/config_test.go:56`

## 24/11/2026, concorrência entre processos só quebra no Windows

- Sintoma: no Windows, drains simultâneos do spool falham com "cannot find the file" ou "being used by another process", e escritores do SQLite recebem `SQLITE_BUSY` depois do busy timeout; macOS e Linux passam
- Causa: no Windows, rename e leitura disputam o mesmo arquivo (compartilhamento de arquivos) e o fsync por commit é lento, o que alonga o lock de escrita
- Como lidar: o drain pega um flock exclusivo ao lado da pasta do spool e pula se outro processo já drena; a abertura do banco lê a versão do schema sem lock de escrita; o WAL roda com `synchronous(NORMAL)`. Só o CI do Windows reproduz, `GOOS=windows go vet` não pega
- Onde: `internal/spool/spool.go:58`, `internal/store/store.go:68`

## 23/11/2026, `json.Compact` mantém escapes `\uXXXX` e a redação textual não enxerga `<private>`

- Sintoma: `tool_input` com `"\u003cprivate\u003esegredo\u003c/private\u003e"` ou um token com escape no meio é gravado em claro no corpo da observação, embora o título saia redigido
- Causa: `json.Compact` só tira espaços; as strings continuam com os escapes do original, e os padrões de redação procuram o texto literal
- Como lidar: `compactJSON` percorre o JSON por tokens e reescreve cada string com `SetEscapeHTML(false)`, preservando a ordem das chaves (decodificar para `any` reordena as chaves e o corte em 4096 bytes passa a perder campos como `file_path`)
- Onde: `internal/capture/build.go:154`

## 23/11/2026, rotação do `mem.log` falha no Windows enquanto `mem mcp` está aberto

- Sintoma: no Windows o `mem.log` passa de 5 MB e continua crescendo; não aparece `mem.log.1`
- Causa: `mem mcp` (e `mem ui`) mantêm o `mem.log` aberto durante a sessão inteira; no Windows o `os.Rename` de um arquivo aberto por outro processo é negado, e o erro do rename é ignorado de propósito para o hook nunca falhar por causa do log
- Como lidar: aceito por enquanto, o volume é baixo (linhas curtas, só erros e eventos de manutenção); o arquivo volta a rodar quando nenhum `mem mcp` estiver aberto no momento em que um hook abre o log. Para limpar na mão, fechar o Claude Code e apagar `logs/mem.log`
- Onde: `internal/applog/applog.go:22`

## 23/11/2026, `.git` corrompido no wiki faz o git subir para o repositório de fora

- Sintoma: com `.git/HEAD` inválido no diretório do wiki, `git -C <Dir> add/commit` termina com sucesso e grava a memória no repositório git que contém o diretório de dados (por exemplo, `MEM_ENGINE_HOME` dentro de um repo de dotfiles)
- Causa: o git ignora um `.git` que não reconhece como repositório e continua a descoberta nos diretórios pais; `-C` não impede isso
- Como lidar: `Repo.run` passa `GIT_CEILING_DIRECTORIES=<pai do Dir>`, então o git só aceita o próprio `Dir` e falha com `fatal: not a git repository`; `TestCommitFailureCarriesGitOutput` cobre o caso com um repo externo
- Onde: `internal/wiki/repo.go:226`

## 23/11/2026, `journal_mode(WAL)` devolve SQLITE_BUSY sem respeitar `busy_timeout`

- Sintoma: vários processos abrindo o mesmo `mem.db` novo ao mesmo tempo; alguns falham na hora com `database is locked (5) (SQLITE_BUSY)`, sem esperar os 2 s do `busy_timeout`
- Causa: a troca de rollback journal para WAL num arquivo novo escala o lock e o SQLite devolve BUSY sem chamar o busy handler; a falha acontece ao abrir a conexão (pragmas do DSN), antes do `BEGIN IMMEDIATE`. Sem o pragma de WAL no DSN o mesmo teste passa 100 vezes seguidas
- Como lidar: `OpenWith` repete a migração enquanto o erro for SQLITE_BUSY, até o prazo do `busy_timeout`; `TestConcurrentOpen` com `-count=20 -race` cobre o caso
- Onde: `internal/store/store.go:52`
