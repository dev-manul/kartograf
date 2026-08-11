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
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/dev-manul/kartograf/internal/core/model"
)

// schemaVersion is bumped on any incompatible schema change; a version
// mismatch drops and recreates the database (it is cheap to rebuild).
const schemaVersion = 5

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
	doc        TEXT NOT NULL DEFAULT '',
	words      TEXT NOT NULL DEFAULT '' -- camelCase-split name for FTS
);
CREATE TABLE IF NOT EXISTS imports (
	file_id INTEGER NOT NULL,
	alias   TEXT NOT NULL,
	fqn     TEXT NOT NULL,
	kind    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS edges (
	file_id  INTEGER NOT NULL,
	from_fqn TEXT NOT NULL, -- '' = file-level code
	kind     TEXT NOT NULL,
	to_fqn   TEXT NOT NULL,
	resolved INTEGER NOT NULL DEFAULT 1,
	line     INTEGER NOT NULL DEFAULT 0
);

-- ext_edges hold enrichment data (PHPStan / go-types type-inference
-- exports). They are replaced wholesale per source on import and are
-- not touched by file reindexing.
CREATE TABLE IF NOT EXISTS ext_edges (
	source   TEXT NOT NULL, -- 'phpstan' | 'go-types' | ...
	from_fqn TEXT NOT NULL,
	kind     TEXT NOT NULL,
	to_fqn   TEXT NOT NULL,
	file     TEXT NOT NULL DEFAULT '',
	line     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ext_edges_from ON ext_edges(from_fqn, kind);
CREATE INDEX IF NOT EXISTS idx_ext_edges_to   ON ext_edges(to_fqn, kind);

-- all_edges is the read-side union of AST edges and enrichment edges.
-- Dropped and recreated on every open so definition changes reach
-- existing databases without a schema-version rebuild.
DROP VIEW IF EXISTS all_edges;
CREATE VIEW all_edges AS
	SELECT e.from_fqn AS from_fqn, e.kind AS kind, e.to_fqn AS to_fqn,
		e.resolved AS resolved, f.path AS file, e.line AS line,
		'ast' AS source
	FROM edges e JOIN files f ON f.id = e.file_id
	UNION ALL
	SELECT x.from_fqn, x.kind, x.to_fqn, 1 AS resolved, x.file, x.line,
		x.source AS source
	FROM ext_edges x;

CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
	name, fqn, doc, words,
	content='symbols', content_rowid='rowid'
);
` + secondaryDDL

// secondaryDDL holds the indexes and FTS-sync triggers. Bulk loads
// drop them first and recreate them after the data is in (plus one
// FTS rebuild), which is much cheaper than maintaining them row by
// row across ~10^6 inserts.
const secondaryDDL = `
CREATE INDEX IF NOT EXISTS idx_symbols_file      ON symbols(file_id);
CREATE INDEX IF NOT EXISTS idx_symbols_fqn       ON symbols(fqn);
CREATE INDEX IF NOT EXISTS idx_symbols_name      ON symbols(name);
CREATE INDEX IF NOT EXISTS idx_symbols_container ON symbols(container);
CREATE INDEX IF NOT EXISTS idx_imports_file      ON imports(file_id);
CREATE INDEX IF NOT EXISTS idx_edges_file        ON edges(file_id);
CREATE INDEX IF NOT EXISTS idx_edges_from        ON edges(from_fqn, kind);
CREATE INDEX IF NOT EXISTS idx_edges_to          ON edges(to_fqn, kind);
CREATE TRIGGER IF NOT EXISTS symbols_ai AFTER INSERT ON symbols BEGIN
	INSERT INTO symbols_fts(rowid, name, fqn, doc, words)
	VALUES (new.rowid, new.name, new.fqn, new.doc, new.words);
END;
CREATE TRIGGER IF NOT EXISTS symbols_ad AFTER DELETE ON symbols BEGIN
	INSERT INTO symbols_fts(symbols_fts, rowid, name, fqn, doc, words)
	VALUES ('delete', old.rowid, old.name, old.fqn, old.doc, old.words);
END;
`

// dropSecondaryDDL mirrors secondaryDDL for bulk loads.
const dropSecondaryDDL = `
DROP INDEX IF EXISTS idx_symbols_file;
DROP INDEX IF EXISTS idx_symbols_fqn;
DROP INDEX IF EXISTS idx_symbols_name;
DROP INDEX IF EXISTS idx_symbols_container;
DROP INDEX IF EXISTS idx_imports_file;
DROP INDEX IF EXISTS idx_edges_file;
DROP INDEX IF EXISTS idx_edges_from;
DROP INDEX IF EXISTS idx_edges_to;
DROP TRIGGER IF EXISTS symbols_ai;
DROP TRIGGER IF EXISTS symbols_ad;
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
		"?_journal_mode=WAL" +
		"&_synchronous=NORMAL" +
		"&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path}
	// mmap speeds up cold reads considerably on big databases (the
	// first query after OS page-cache eviction).
	if _, err := db.Exec(`PRAGMA mmap_size = 1073741824`); err != nil {
		db.Close()
		return nil, err
	}
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

// batchRows is the multi-row INSERT flush threshold. 500 rows of the
// widest table (symbols, 14 cols) is 7000 placeholders — well under
// SQLite's variable limit (32766).
const batchRows = 500

// batch accumulates rows for one table and flushes them as multi-row
// INSERTs.
type batch struct {
	insert string // "INSERT INTO t (a, b) VALUES "
	nCols  int
	args   []any
	nRows  int
}

func newBatch(insert string, nCols int) *batch {
	return &batch{insert: insert, nCols: nCols}
}

// Writer batches index mutations in a single transaction with
// multi-row inserts (the insert path is hot on cold runs). In bulk
// mode (fresh database) secondary indexes and FTS triggers are
// dropped up front and rebuilt once at Commit.
type Writer struct {
	tx         *sql.Tx
	stmts      map[string]*sql.Stmt
	bulk       bool
	nextFileID int64

	files, symbols, imports, edges *batch
}

func (s *Store) BeginWrite() (*Writer, error) { return s.beginWrite(false) }

// BeginBulkWrite is BeginWrite for loading into an empty index: it
// defers index/FTS maintenance to Commit. Callers must not rely on
// per-file deletes (there is nothing to delete in a fresh database).
func (s *Store) BeginBulkWrite() (*Writer, error) { return s.beginWrite(true) }

func (s *Store) beginWrite(bulk bool) (*Writer, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	w := &Writer{
		tx:    tx,
		stmts: map[string]*sql.Stmt{},
		bulk:  bulk,
		files: newBatch(`INSERT INTO files (id, path, lang, hash, size, mtime_ns, vendor, has_errors, indexed_at) VALUES `, 9),
		symbols: newBatch(`INSERT INTO symbols (file_id, sym_id, lang, kind, name, fqn, container,
			start_line, start_col, end_line, end_col, signature, doc, words) VALUES `, 14),
		imports: newBatch(`INSERT INTO imports (file_id, alias, fqn, kind) VALUES `, 4),
		edges:   newBatch(`INSERT INTO edges (file_id, from_fqn, kind, to_fqn, resolved, line) VALUES `, 6),
	}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM files`).Scan(&w.nextFileID); err != nil {
		tx.Rollback()
		return nil, err
	}
	if bulk {
		if _, err := tx.Exec(dropSecondaryDDL); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	return w, nil
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

func (w *Writer) add(b *batch, vals ...any) error {
	b.args = append(b.args, vals...)
	b.nRows++
	if b.nRows >= batchRows {
		return w.flush(b)
	}
	return nil
}

func (w *Writer) flush(b *batch) error {
	if b.nRows == 0 {
		return nil
	}
	row := "(" + strings.Repeat("?, ", b.nCols-1) + "?)"
	query := b.insert + strings.Repeat(row+", ", b.nRows-1) + row
	// Full-size batches share one prepared statement; odd-sized tails
	// are one-off but rare.
	if _, err := w.exec(query, b.args...); err != nil {
		return err
	}
	b.args = b.args[:0]
	b.nRows = 0
	return nil
}

func (w *Writer) flushAll() error {
	for _, b := range []*batch{w.files, w.symbols, w.imports, w.edges} {
		if err := w.flush(b); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) Commit() error {
	if err := w.flushAll(); err != nil {
		return err
	}
	if w.bulk {
		// One FTS rebuild from the content table, then indexes —
		// still inside the same transaction.
		if _, err := w.tx.Exec(`INSERT INTO symbols_fts(symbols_fts) VALUES ('rebuild')`); err != nil {
			return err
		}
		if _, err := w.tx.Exec(secondaryDDL); err != nil {
			return err
		}
	}
	return w.tx.Commit()
}

func (w *Writer) Rollback() error { return w.tx.Rollback() }

// ReplaceFile removes any previous data for the file and inserts the
// fresh extraction result.
func (w *Writer) ReplaceFile(d FileData) error {
	if !w.bulk {
		if err := w.deleteFile(d.FI.Path); err != nil {
			return err
		}
	}
	w.nextFileID++
	fileID := w.nextFileID
	if err := w.add(w.files,
		fileID, d.FI.Path, d.FI.Lang, d.Hash, d.Size, d.MtimeNs,
		boolInt(d.Vendor), boolInt(d.FI.HasErrors), d.Indexed,
	); err != nil {
		return err
	}
	for _, sym := range d.FI.Symbols {
		if err := w.add(w.symbols,
			fileID, sym.ID, sym.Lang, string(sym.Kind), sym.Name, sym.FQN, sym.Container,
			sym.Range.StartLine, sym.Range.StartCol, sym.Range.EndLine, sym.Range.EndCol,
			sym.Signature, sym.Doc, SplitWords(sym.Name),
		); err != nil {
			return err
		}
	}
	for _, imp := range d.FI.Imports {
		if err := w.add(w.imports, fileID, imp.Alias, imp.FQN, imp.Kind); err != nil {
			return err
		}
	}
	for _, ref := range d.FI.Refs {
		if err := w.add(w.edges,
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

// ExtEdge is one enrichment edge (exact by definition: it comes from
// a full type-inference tool).
type ExtEdge struct {
	From string `json:"from"`
	Kind string `json:"kind"`
	To   string `json:"to"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

// ReplaceExtEdges swaps the whole enrichment edge set of one source.
func (s *Store) ReplaceExtEdges(source string, rows []ExtEdge) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ext_edges WHERE source = ?`, source); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO ext_edges (source, from_fqn, kind, to_fqn, file, line)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := st.Exec(source, r.From, r.Kind, r.To, r.File, r.Line); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ImportedEnrichSources lists enrichment sources that currently have
// edges in the store.
func (s *Store) ImportedEnrichSources() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT source FROM ext_edges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// EnrichStats returns enrichment edge counts per source.
func (s *Store) EnrichStats() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT source, COUNT(*) FROM ext_edges GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return nil, err
		}
		out[src] = n
	}
	return out, rows.Err()
}

// LangCounts returns non-vendor indexed file counts per language.
func (s *Store) LangCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT lang, COUNT(*) FROM files WHERE vendor = 0 GROUP BY lang`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var lang string
		var n int
		if err := rows.Scan(&lang, &n); err != nil {
			return nil, err
		}
		out[lang] = n
	}
	return out, rows.Err()
}

// Meta reads an arbitrary metadata value ("" when absent).
func (s *Store) Meta(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetMeta stores an arbitrary metadata value.
func (s *Store) SetMeta(key, value string) error { return s.setMeta(key, value) }

// IndexedPaths returns the set of indexed file paths (for mapping
// external tool output paths onto the index).
func (s *Store) IndexedPaths() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT path FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		m[p] = true
	}
	return m, rows.Err()
}

// ProjectPaths returns non-vendor indexed file paths, optionally
// narrowed by a path prefix.
func (s *Store) ProjectPaths(prefix string) ([]string, error) {
	q := `SELECT path FROM files WHERE vendor = 0`
	var args []any
	if prefix != "" {
		q += ` AND path LIKE ? ESCAPE '!'`
		args = append(args, strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(prefix)+"%")
	}
	q += ` ORDER BY path`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SplitWords breaks an identifier into lowercase words for full-text
// search: "BannerHTTPRepository_v2" -> "banner http repository v2".
func SplitWords(name string) string {
	var out []rune
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-':
			r = ' '
		case i > 0 && isUpper(r):
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && isLower(runes[i+1])
			if isLower(prev) || isDigit(prev) || (isUpper(prev) && nextLower) {
				out = append(out, ' ')
			}
		case i > 0 && isDigit(r) && !isDigit(runes[i-1]):
			out = append(out, ' ')
		}
		out = append(out, toLower(r))
	}
	return string(out)
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }
func toLower(r rune) rune {
	if isUpper(r) {
		return r + ('a' - 'A')
	}
	return r
}
