package store

const SchemaVersion = 1

var migrations = []string{schemaV1}

const schemaV1 = `
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

CREATE TRIGGER observations_ai AFTER INSERT ON observations BEGIN
  INSERT INTO observations_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER observations_ad AFTER DELETE ON observations BEGIN
  INSERT INTO observations_fts(observations_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
END;
CREATE TRIGGER observations_au AFTER UPDATE ON observations BEGIN
  INSERT INTO observations_fts(observations_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
  INSERT INTO observations_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, title, summary, body, tags) VALUES (new.rowid, new.title, new.summary, new.body, new.tags);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, summary, body, tags) VALUES ('delete', old.rowid, old.title, old.summary, old.body, old.tags);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, summary, body, tags) VALUES ('delete', old.rowid, old.title, old.summary, old.body, old.tags);
  INSERT INTO memories_fts(rowid, title, summary, body, tags) VALUES (new.rowid, new.title, new.summary, new.body, new.tags);
END;
`
