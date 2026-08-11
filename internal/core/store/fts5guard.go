//go:build !sqlite_fts5

package store

// kartograf requires FTS5 support in mattn/go-sqlite3, which is only
// compiled in with the sqlite_fts5 build tag. Build via the Makefile,
// or pass the tag yourself:
//
//	go build -tags sqlite_fts5 ./...
//	go install -tags sqlite_fts5 ./cmd/kartograf
//
// This intentionally fails compilation when the tag is missing —
// without it the binary would die at runtime on "no such module: fts5".
var _ = BUILD_WITH_TAG_sqlite_fts5__SEE_internal_core_store_fts5guard_go
