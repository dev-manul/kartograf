package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dev-manul/kartograf/internal/taskctx"
)

const taskContextInstructions = "If a kartograf_task block is already in the conversation, do not call get_task_context. At the end of a turn, call put_task_context only when the goal, a decision, or the next step changed. Keep the note to a few short lines (goal, status, next, decisions). Skip questions and lookups, do not mention the save in the reply, and do not spend a separate turn on it."

type taskContextIn struct {
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
}

type putTaskContextIn struct {
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
	Body   string `json:"body" jsonschema:"replacement note, a few short lines (goal, status, next, decisions), 800 bytes max. A blank body deletes the note"`
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
		Description: "Load the working note for a git branch (current checkout when branch is omitted). " +
			"Skip this call when a kartograf_task block is already in the conversation. " +
			"Returns branch, updatedAt, and body (empty when no note is saved yet).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		note, err := taskctx.Get(root, in.Branch)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(note), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "put_task_context",
		Description: "Replace the working note for a git branch (current checkout when branch is omitted). " +
			"Call at most once, at the end of a turn, and only when the goal, a decision, or the next step changed. " +
			"Skip the call otherwise. Do not mention it in the reply. " +
			fmt.Sprintf("A few short lines, %d bytes max. A blank body deletes the note.", taskctx.MaxBody),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in putTaskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		note, err := taskctx.Put(root, in.Branch, in.Body)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(note), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_task_contexts",
		Description: "List branches that have a saved note: name, updatedAt, and the first line. " +
			"Use this for a branch other than the one checked out.",
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
