# GOTCHAS

## 23/11/2026, `journal_mode(WAL)` devolve SQLITE_BUSY sem respeitar `busy_timeout`

- Sintoma: vários processos abrindo o mesmo `mem.db` novo ao mesmo tempo; alguns falham na hora com `database is locked (5) (SQLITE_BUSY)`, sem esperar os 2 s do `busy_timeout`
- Causa: a troca de rollback journal para WAL num arquivo novo escala o lock e o SQLite devolve BUSY sem chamar o busy handler; a falha acontece ao abrir a conexão (pragmas do DSN), antes do `BEGIN IMMEDIATE`. Sem o pragma de WAL no DSN o mesmo teste passa 100 vezes seguidas
- Como lidar: `OpenWith` repete a migração enquanto o erro for SQLITE_BUSY, até o prazo do `busy_timeout`; `TestConcurrentOpen` com `-count=20 -race` cobre o caso
- Onde: `internal/store/store.go:45`
