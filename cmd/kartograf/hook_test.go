package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatStopOutput(t *testing.T) {
	if got := formatStopOutput(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
	got := formatStopOutput("write the handoff")
	var payload map[string]string
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["decision"] != "block" || !strings.Contains(payload["reason"], "handoff") {
		t.Fatalf("payload = %v", payload)
	}
}

func TestFormatHookOutput(t *testing.T) {
	if got := formatHookOutput(false, ""); got != "" {
		t.Fatalf("claude empty = %q", got)
	}
	if got := formatHookOutput(false, "note\n"); got != "note\n" {
		t.Fatalf("claude text = %q", got)
	}
	got := formatHookOutput(true, "")
	var payload map[string]any
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["continue"] != true {
		t.Fatalf("payload = %v", payload)
	}
	if _, ok := payload["additional_context"]; ok {
		t.Fatal("empty cursor hook should omit additional_context")
	}
	got = formatHookOutput(true, "<kartograf_task>\n")
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload["additional_context"].(string), "kartograf_task") {
		t.Fatalf("payload = %v", payload)
	}
}
