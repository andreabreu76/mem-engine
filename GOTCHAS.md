# GOTCHAS

## 23/11/2026, `.git` corrompido no wiki faz o git subir para o repositório de fora

- Sintoma: com `.git/HEAD` inválido no diretório do wiki, `git -C <Dir> add/commit` termina com sucesso e grava a memória no repositório git que contém o diretório de dados (por exemplo, `MEM_ENGINE_HOME` dentro de um repo de dotfiles)
- Causa: o git ignora um `.git` que não reconhece como repositório e continua a descoberta nos diretórios pais; `-C` não impede isso
- Como lidar: `Repo.run` passa `GIT_CEILING_DIRECTORIES=<pai do Dir>`, então o git só aceita o próprio `Dir` e falha com `fatal: not a git repository`; `TestCommitFailureCarriesGitOutput` cobre o caso com um repo externo
- Onde: `internal/wiki/repo.go:210`

## 23/11/2026, `journal_mode(WAL)` devolve SQLITE_BUSY sem respeitar `busy_timeout`

- Sintoma: vários processos abrindo o mesmo `mem.db` novo ao mesmo tempo; alguns falham na hora com `database is locked (5) (SQLITE_BUSY)`, sem esperar os 2 s do `busy_timeout`
- Causa: a troca de rollback journal para WAL num arquivo novo escala o lock e o SQLite devolve BUSY sem chamar o busy handler; a falha acontece ao abrir a conexão (pragmas do DSN), antes do `BEGIN IMMEDIATE`. Sem o pragma de WAL no DSN o mesmo teste passa 100 vezes seguidas
- Como lidar: `OpenWith` repete a migração enquanto o erro for SQLITE_BUSY, até o prazo do `busy_timeout`; `TestConcurrentOpen` com `-count=20 -race` cobre o caso
- Onde: `internal/store/store.go:45`
