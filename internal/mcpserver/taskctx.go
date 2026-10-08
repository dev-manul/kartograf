package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dev-manul/kartograf/internal/taskctx"
)

const taskContextInstructions = "Task notes are a handoff so a new chat can resume the work days later. When you pause or finish work on a branch, call put_task_context once with: goal, what changed (key files and symbols), decisions and why, what is left, how to verify. Skip the call when that handoff would be unchanged, and do not mention it in the reply. If a kartograf_task block for this branch is already present, do not call get_task_context. If the user brings follow-ups and the current branch has no note, read the matching branch from kartograf_tasks or list_task_contexts before exploring the code."

type taskContextIn struct {
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
}

type putTaskContextIn struct {
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
	Body   string `json:"body" jsonschema:"handoff for a later chat: goal, what changed (files and symbols), decisions and why, what is left, how to verify. A blank body deletes the note"`
}

type taskContextOut struct {
	Branch    string `json:"branch"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	Body      string `json:"body"`
}

type listTaskContextIn struct{}

type taskContextSummary struct {
	Branch    string `json:"branch"`
	UpdatedAt string `json:"updatedAt"`
	Preview   string `json:"preview"`
}

type listTaskContextOut struct {
	Results []taskContextSummary `json:"results"`
}

func registerTaskContext(s *mcp.Server, root string) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_task_context",
		Description: "Load the handoff note for a git branch (current checkout when branch is omitted): " +
			"what was done, key files and symbols, decisions, and what is left. " +
			"Skip this call when a kartograf_task block for that branch is already in the conversation. " +
			"Pass branch to open a note from another branch listed in kartograf_tasks.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		note, err := taskctx.Get(root, in.Branch)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(note), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "put_task_context",
		Description: "Replace the handoff note for a git branch (current checkout when branch is omitted). " +
			"Call once when you pause or finish the work, so a new chat can resume it days later. " +
			"Cover goal, what changed (key files and symbols), decisions and why, what is left, and how to verify. " +
			"Skip the call when the handoff would be unchanged. Do not mention it in the reply. " +
			fmt.Sprintf("%d bytes max. A blank body deletes the note.", taskctx.MaxBody),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in putTaskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		note, err := taskctx.Put(root, in.Branch, in.Body)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(note), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_task_contexts",
		Description: "List branches that have a handoff note: name, updatedAt, and the first line. " +
			"Use this when follow-up work arrives and the current branch has no note.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listTaskContextIn) (*mcp.CallToolResult, listTaskContextOut, error) {
		list, err := taskctx.List(root)
		if err != nil {
			return nil, listTaskContextOut{}, err
		}
		out := make([]taskContextSummary, 0, len(list))
		for _, s := range list {
			out = append(out, taskContextSummary{
				Branch:    s.Branch,
				UpdatedAt: formatUpdated(s.UpdatedAt),
				Preview:   s.Preview,
			})
		}
		return nil, listTaskContextOut{Results: out}, nil
	})
}

func taskContextNote(note taskctx.Note) taskContextOut {
	return taskContextOut{
		Branch:    note.Branch,
		UpdatedAt: formatUpdated(note.UpdatedAt),
		Body:      note.Body,
	}
}

func formatUpdated(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
