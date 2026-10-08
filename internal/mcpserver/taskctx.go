package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dev-manul/kartograf/internal/taskctx"
)

const taskContextInstructions = "At the start of work on a git branch, call get_task_context to load the note saved for that branch (goal, decisions, files, next step). When the plan changes, call put_task_context with the full updated note — read the current note first and include anything that should be kept. Use list_task_contexts to find notes for other branches."

type taskContextIn struct {
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
}

type putTaskContextIn struct {
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
	Body   string `json:"body" jsonschema:"full markdown note replacing any previous text for this branch; a blank body deletes the note. Read get_task_context first and include whatever should be kept. 16 KiB max"`
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
		Description: "Load the working note for a git branch (the branch checked out in the project root when branch is omitted): " +
			"goal, decisions, files, and where work stopped. Call this at the start of a task so the note survives a new chat " +
			"or a branch switch. Returns branch, updatedAt, and body (empty when no note is saved yet).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		note, err := taskctx.Get(root, in.Branch)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(note), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "put_task_context",
		Description: "Replace the working note for a git branch (the branch checked out in the project root when branch is omitted). " +
			"Read get_task_context first and write back the full note, including anything that should be kept. " +
			"A blank body deletes the note. Call this when the plan, decisions, or next step change. " +
			fmt.Sprintf("Body is markdown, %d bytes max.", taskctx.MaxBody),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in putTaskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		note, err := taskctx.Put(root, in.Branch, in.Body)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(note), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_task_contexts",
		Description: "List git branches that have a saved working note: branch name, updatedAt, and the first line of the body. " +
			"Use this to find context for a branch other than the one checked out.",
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
