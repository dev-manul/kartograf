package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

// An outdated database whose tables lack columns the current DDL
// indexes must still open (by rebuild) — the version check has to run
// before the schema is applied.
func TestOpenRebuildsOutdatedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO meta VALUES ('schema_version', '` + strconv.Itoa(schemaVersion-1) + `');
		CREATE TABLE ext_edges (source TEXT NOT NULL, from_fqn TEXT NOT NULL, kind TEXT NOT NULL,
			to_fqn TEXT NOT NULL, file TEXT NOT NULL DEFAULT '', line INTEGER NOT NULL DEFAULT 0);
		INSERT INTO ext_edges VALUES ('phpstan', 'A', 'calls', 'B', 'a.php', 1);`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path, "/project")
	if err != nil {
		t.Fatalf("open outdated db: %v", err)
	}
	defer s.Close()
	ver, err := s.metaInt("schema_version")
	if err != nil || ver != schemaVersion {
		t.Fatalf("schema_version = %d (%v), want %d", ver, err, schemaVersion)
	}
	origins, err := s.ImportedEnrichOrigins()
	if err != nil || len(origins) != 0 {
		t.Fatalf("legacy edges survived rebuild: %v (%v)", origins, err)
	}
	if err := s.ReplaceExtEdges("x/.kartograf/enrich.phpstan.jsonl", "phpstan", []ExtEdge{{From: "A", Kind: "calls", To: "B"}}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFreshAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path, "/project")
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		root, _ := s.Meta("project_root")
		if root != "/project" {
			t.Fatalf("project_root = %q", root)
		}
		s.Close()
	}
}
