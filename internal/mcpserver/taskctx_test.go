package mcpserver

import "testing"

func TestToolsRegister(t *testing.T) {
	// Registration infers JSON schemas and panics on a tool the client
	// would reject. A nil query engine is enough: handlers are not called.
	if New(nil, t.TempDir(), "test") == nil {
		t.Fatal("nil server")
	}
}
