// Package store persists the code index in SQLite (with an FTS5 index
// over symbol names/FQNs/docs). The database is a derived artifact:
// it lives in the user cache dir, is keyed by project root, and is
// silently rebuilt when the schema version changes.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/model"
)

// schemaVersion is bumped on any incompatible schema change; a version
// mismatch drops and recreates the database (it is cheap to rebuild).
const schemaVersion = 3

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
	id         INTEGER PRIMARY KEY,
	path       TEXT NOT NULL UNIQUE, -- root-relative, forward slashes
	lang       TEXT NOT NULL,
	hash       TEXT NOT NULL,        -- sha256 of content
	size       INTEGER NOT NULL,
	mtime_ns   INTEGER NOT NULL DEFAULT 0,
	vendor     INTEGER NOT NULL DEFAULT 0,
	has_errors INTEGER NOT NULL DEFAULT 0,
	indexed_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS symbols (
	file_id    INTEGER NOT NULL,
	sym_id     TEXT NOT NULL,
	lang       TEXT NOT NULL,
	kind       TEXT NOT NULL,
	name       TEXT NOT NULL,
	fqn        TEXT NOT NULL,
	container  TEXT NOT NULL DEFAULT '',
	start_line INTEGER NOT NULL,
	start_col  INTEGER NOT NULL,
	end_line   INTEGER NOT NULL,
	end_col    INTEGER NOT NULL,
	signature  TEXT NOT NULL DEFAULT '',
	doc        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_symbols_file ON symbols(file_id);
CREATE INDEX IF NOT EXISTS idx_symbols_fqn  ON symbols(fqn);
CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);

CREATE TABLE IF NOT EXISTS imports (
	file_id INTEGER NOT NULL,
	alias   TEXT NOT NULL,
	fqn     TEXT NOT NULL,
	kind    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_imports_file ON imports(file_id);

CREATE TABLE IF NOT EXISTS edges (
	file_id  INTEGER NOT NULL,
	from_fqn TEXT NOT NULL, -- '' = file-level code
	kind     TEXT NOT NULL,
	to_fqn   TEXT NOT NULL,
	resolved INTEGER NOT NULL DEFAULT 1,
	line     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_edges_file ON edges(file_id);
CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_fqn, kind);
CREATE INDEX IF NOT EXISTS idx_edges_to   ON edges(to_fqn, kind);

CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
	name, fqn, doc,
	content='symbols', content_rowid='rowid'
);
CREATE TRIGGER IF NOT EXISTS symbols_ai AFTER INSERT ON symbols BEGIN
	INSERT INTO symbols_fts(rowid, name, fqn, doc)
	VALUES (new.rowid, new.name, new.fqn, new.doc);
END;
CREATE TRIGGER IF NOT EXISTS symbols_ad AFTER DELETE ON symbols BEGIN
	INSERT INTO symbols_fts(symbols_fts, rowid, name, fqn, doc)
	VALUES ('delete', old.rowid, old.name, old.fqn, old.doc);
END;
`

type Store struct {
	db   *sql.DB
	path string
}

// DefaultPath returns the cache location of the index database for a
// project root: <UserCacheDir>/kartograf/<base>-<hash8>/index.db.
func DefaultPath(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	dir := filepath.Base(abs) + "-" + hex.EncodeToString(sum[:4])
	return filepath.Join(cache, "kartograf", dir, "index.db"), nil
}

// Open opens (creating or rebuilding if needed) the index database.
func Open(path, projectRoot string) (*Store, error) {
	return open(path, projectRoot, true)
}

func open(path, projectRoot string, retry bool) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: init schema: %w", err)
	}

	ver, err := s.metaInt("schema_version")
	if err != nil {
		db.Close()
		return nil, err
	}
	switch {
	case ver == 0: // fresh database
		if err := s.setMeta("schema_version", strconv.Itoa(schemaVersion)); err != nil {
			db.Close()
			return nil, err
		}
		if err := s.setMeta("project_root", projectRoot); err != nil {
			db.Close()
			return nil, err
		}
	case ver != schemaVersion:
		db.Close()
		if !retry {
			return nil, fmt.Errorf("store: schema version %d after rebuild, want %d", ver, schemaVersion)
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			os.Remove(path + suffix)
		}
		return open(path, projectRoot, false)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }
func (s *Store) DB() *sql.DB  { return s.db }

func (s *Store) metaInt(key string) (int, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

func (s *Store) setMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// FileMeta is the change-detection state of an indexed file.
type FileMeta struct {
	Hash    string
	Size    int64
	MtimeNs int64
}

// FileStates returns path -> meta for every indexed file; the indexer
// diffs the working tree against it (mtime+size fast path, hash as the
// source of truth).
func (s *Store) FileStates() (map[string]FileMeta, error) {
	rows, err := s.db.Query(`SELECT path, hash, size, mtime_ns FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]FileMeta{}
	for rows.Next() {
		var p string
		var fm FileMeta
		if err := rows.Scan(&p, &fm.Hash, &fm.Size, &fm.MtimeNs); err != nil {
			return nil, err
		}
		m[p] = fm
	}
	return m, rows.Err()
}

type Stats struct {
	Files   int
	Symbols int
}

func (s *Store) Stats() (Stats, error) {
	var st Stats
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&st.Files); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM symbols`).Scan(&st.Symbols); err != nil {
		return st, err
	}
	return st, nil
}

// FileData is one file's extraction result plus bookkeeping.
type FileData struct {
	FI      *model.FileIndex
	Hash    string
	Size    int64
	MtimeNs int64
	Vendor  bool
	Indexed int64 // unix seconds
}

// Writer batches index mutations in a single transaction with
// prepared statements (the insert path is hot on cold runs).
type Writer struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func (s *Store) BeginWrite() (*Writer, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	return &Writer{tx: tx, stmts: map[string]*sql.Stmt{}}, nil
}

func (w *Writer) stmt(query string) (*sql.Stmt, error) {
	if st, ok := w.stmts[query]; ok {
		return st, nil
	}
	st, err := w.tx.Prepare(query)
	if err != nil {
		return nil, err
	}
	w.stmts[query] = st
	return st, nil
}

func (w *Writer) exec(query string, args ...any) (sql.Result, error) {
	st, err := w.stmt(query)
	if err != nil {
		return nil, err
	}
	return st.Exec(args...)
}

func (w *Writer) Commit() error   { return w.tx.Commit() }
func (w *Writer) Rollback() error { return w.tx.Rollback() }

// ReplaceFile removes any previous data for the file and inserts the
// fresh extraction result.
func (w *Writer) ReplaceFile(d FileData) error {
	if err := w.deleteFile(d.FI.Path); err != nil {
		return err
	}
	res, err := w.exec(
		`INSERT INTO files (path, lang, hash, size, mtime_ns, vendor, has_errors, indexed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.FI.Path, d.FI.Lang, d.Hash, d.Size, d.MtimeNs, boolInt(d.Vendor), boolInt(d.FI.HasErrors), d.Indexed,
	)
	if err != nil {
		return err
	}
	fileID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, sym := range d.FI.Symbols {
		if _, err := w.exec(
			`INSERT INTO symbols (file_id, sym_id, lang, kind, name, fqn, container,
				start_line, start_col, end_line, end_col, signature, doc)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			fileID, sym.ID, sym.Lang, string(sym.Kind), sym.Name, sym.FQN, sym.Container,
			sym.Range.StartLine, sym.Range.StartCol, sym.Range.EndLine, sym.Range.EndCol,
			sym.Signature, sym.Doc,
		); err != nil {
			return err
		}
	}
	for _, imp := range d.FI.Imports {
		if _, err := w.exec(
			`INSERT INTO imports (file_id, alias, fqn, kind) VALUES (?, ?, ?, ?)`,
			fileID, imp.Alias, imp.FQN, imp.Kind,
		); err != nil {
			return err
		}
	}
	for _, ref := range d.FI.Refs {
		if _, err := w.exec(
			`INSERT INTO edges (file_id, from_fqn, kind, to_fqn, resolved, line)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			fileID, ref.From, string(ref.Kind), ref.To, boolInt(ref.Resolved), ref.Line,
		); err != nil {
			return err
		}
	}
	return nil
}

// TouchFile refreshes size/mtime for a file whose content hash is
// unchanged (e.g. after a branch switch and back), so future runs take
// the stat fast path again.
func (w *Writer) TouchFile(path string, size, mtimeNs int64) error {
	_, err := w.exec(`UPDATE files SET size = ?, mtime_ns = ? WHERE path = ?`, size, mtimeNs, path)
	return err
}

// DeleteFile removes a file and all its data from the index.
func (w *Writer) DeleteFile(path string) error { return w.deleteFile(path) }

func (w *Writer) deleteFile(path string) error {
	sel, err := w.stmt(`SELECT id FROM files WHERE path = ?`)
	if err != nil {
		return err
	}
	var fileID int64
	err = sel.QueryRow(path).Scan(&fileID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	// Explicit deletes (no FK cascade) so the FTS delete trigger fires.
	for _, q := range []string{
		`DELETE FROM symbols WHERE file_id = ?`,
		`DELETE FROM imports WHERE file_id = ?`,
		`DELETE FROM edges WHERE file_id = ?`,
		`DELETE FROM files WHERE id = ?`,
	} {
		if _, err := w.exec(q, fileID); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
