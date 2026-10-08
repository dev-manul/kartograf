// Package usage counts MCP tool calls in a database next to the index.
// The index is rebuilt when its schema changes; this file is not.
// Nothing here is sent anywhere.
package usage

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Open opens (and creates) the usage database at path.
func Open(path string) (*Recorder, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL&_busy_timeout=2000")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS calls (
		at INTEGER NOT NULL,
		tool TEXT NOT NULL,
		bytes INTEGER NOT NULL,
		empty INTEGER NOT NULL,
		saved INTEGER NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Recorder{db: db, start: time.Now()}, nil
}

// Recorder appends one row per tool call.
type Recorder struct {
	db    *sql.DB
	start time.Time
	mu    sync.Mutex
}

// Record stores one tool call. saved is an estimated number of bytes
// the agent did not have to read. Failures are ignored: accounting
// must not break a tool call.
func (r *Recorder) Record(tool string, responseBytes int, empty bool, savedBytes int) {
	if r == nil || tool == "" {
		return
	}
	emptyN := 0
	if empty {
		emptyN = 1
	}
	if savedBytes < 0 {
		savedBytes = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.db.Exec(`INSERT INTO calls (at, tool, bytes, empty, saved) VALUES (?, ?, ?, ?, ?)`,
		time.Now().UnixNano(), tool, responseBytes, emptyN, savedBytes)
}

// Report is a total over a time window.
type Report struct {
	Calls  int
	Bytes  int
	Saved  int
	Empty  int
	ByTool map[string]int
}

// Tokens estimates how many tokens the responses cost to read.
func (r Report) Tokens() int { return r.Bytes / 4 }

// SavedTokens estimates tokens of source the responses stood in for.
func (r Report) SavedTokens() int { return r.Saved / 4 }

// Since summarizes calls at or after t. The zero time means all calls.
func (r *Recorder) Since(t time.Time) (Report, error) {
	var since int64
	if !t.IsZero() {
		since = t.UnixNano()
	}
	rows, err := r.db.Query(`SELECT tool, bytes, empty, saved FROM calls WHERE at >= ?`, since)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	rep := Report{ByTool: map[string]int{}}
	for rows.Next() {
		var tool string
		var bytes, empty, saved int
		if err := rows.Scan(&tool, &bytes, &empty, &saved); err != nil {
			return Report{}, err
		}
		rep.Calls++
		rep.Bytes += bytes
		rep.Saved += saved
		rep.Empty += empty
		rep.ByTool[tool]++
	}
	return rep, rows.Err()
}

// SessionLine is one log line for the calls recorded since Open.
func (r *Recorder) SessionLine(dollarsPerMillion float64) string {
	rep, err := r.Since(r.start)
	if err != nil || rep.Calls == 0 {
		return ""
	}
	line := fmt.Sprintf("this session: %d calls, ~%d response tokens, ~%d tokens estimated not read (%d empty)",
		rep.Calls, rep.Tokens(), rep.SavedTokens(), rep.Empty)
	if dollarsPerMillion > 0 && rep.SavedTokens() > 0 {
		line += fmt.Sprintf(", about $%.4f at $%.2f/1M", float64(rep.SavedTokens())/1e6*dollarsPerMillion, dollarsPerMillion)
	}
	return line
}

// Format renders a report for the stats command.
func Format(rep Report, dollarsPerMillion float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "calls: %d\n", rep.Calls)
	fmt.Fprintf(&b, "empty: %d\n", rep.Empty)
	fmt.Fprintf(&b, "response: %d bytes, ~%d tokens\n", rep.Bytes, rep.Tokens())
	fmt.Fprintf(&b, "estimated unread: %d bytes, ~%d tokens\n", rep.Saved, rep.SavedTokens())
	if dollarsPerMillion > 0 {
		fmt.Fprintf(&b, "estimated unread at $%.2f/1M: $%.4f\n", dollarsPerMillion, float64(rep.SavedTokens())/1e6*dollarsPerMillion)
	}
	fmt.Fprintf(&b, "by tool:\n")
	for tool, n := range rep.ByTool {
		fmt.Fprintf(&b, "  %s %d\n", tool, n)
	}
	return b.String()
}

func (r *Recorder) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
