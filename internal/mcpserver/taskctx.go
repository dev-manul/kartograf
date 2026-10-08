package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dev-manul/kartograf/internal/core/query"
	"github.com/dev-manul/kartograf/internal/taskctx"
)

const taskContextInstructions = "Task notes are a handoff so a new chat can resume the work days later. When you pause or finish work on a branch, call put_task_context once with: goal, what changed (key files and symbols), decisions and why, what is left, how to verify. Skip the call when that handoff would be unchanged, and do not mention it in the reply. If a kartograf_task block for this branch is already present, do not call get_task_context. If the user brings follow-ups and the current branch has no note, read the matching branch from kartograf_tasks or list_task_contexts before exploring the code. If the user names a task id rather than a branch, call find_task, then get_task_context or branch_changes with that repo and branch. If a kartograf_branch block lists files, call branch_changes for their symbols instead of rereading the branch."

type taskContextIn struct {
	Repo   string `json:"repo,omitempty" jsonschema:"root-relative repository from find_task; omit for the project root"`
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
}

type putTaskContextIn struct {
	Repo   string `json:"repo,omitempty" jsonschema:"root-relative repository from find_task; omit for the project root"`
	Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
	Body   string `json:"body" jsonschema:"handoff for a later chat: goal, what changed (files and symbols), decisions and why, what is left, how to verify. A blank body deletes the note"`
}

type taskContextOut struct {
	Branch     string   `json:"branch"`
	UpdatedAt  string   `json:"updatedAt,omitempty"`
	Body       string   `json:"body"`
	StaleFiles []string `json:"staleFiles,omitempty"`
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

func registerTaskContext(s *mcp.Server, q *query.Engine, root string) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_task_context",
		Description: "Load the handoff note for a git branch (current checkout when branch is omitted): " +
			"what was done, key files and symbols, decisions, and what is left. " +
			"Skip this call when a kartograf_task block for that branch is already in the conversation. " +
			"Pass branch to open a note from another branch listed in kartograf_tasks. " +
			"Pass repo when find_task returned a nested repository. " +
			"staleFiles lists paths from the note that were committed after it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in taskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		dir, err := taskctx.Repo(root, in.Repo)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		note, err := taskctx.Get(dir, in.Branch)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(dir, note), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "put_task_context",
		Description: "Replace the handoff note for a git branch (current checkout when branch is omitted). " +
			"Call once when you pause or finish the work, so a new chat can resume it days later. " +
			"Cover goal, what changed (key files and symbols), decisions and why, what is left, and how to verify. " +
			"Skip the call when the handoff would be unchanged. Do not mention it in the reply. " +
			fmt.Sprintf("%d bytes max. A blank body deletes the note. Pass repo when the branch lives in a nested repository.", taskctx.MaxBody),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in putTaskContextIn) (*mcp.CallToolResult, taskContextOut, error) {
		dir, err := taskctx.Repo(root, in.Repo)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		note, err := taskctx.Put(dir, in.Branch, in.Body)
		if err != nil {
			return nil, taskContextOut{}, err
		}
		return nil, taskContextNote(dir, note), nil
	})

	type branchChangesIn struct {
		Repo   string `json:"repo,omitempty" jsonschema:"root-relative repository from find_task; omit for the project root"`
		Branch string `json:"branch,omitempty" jsonschema:"git branch; omit to use the branch checked out in the project root"`
	}
	type branchFile struct {
		File    string            `json:"file"`
		Symbols []query.SymbolHit `json:"symbols"`
	}
	type branchChangesOut struct {
		Branch    string       `json:"branch"`
		Base      string       `json:"base"`
		Files     []branchFile `json:"files"`
		Truncated bool         `json:"truncated"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "branch_changes",
		Description: "Files and symbols a git branch changed relative to the repository default branch (origin HEAD, else main, else master). " +
			"Use this when the branch has no handoff note, so a later chat does not have to rediscover the work. " +
			"branch defaults to the current checkout. Pass repo when find_task returned a nested repository.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in branchChangesIn) (*mcp.CallToolResult, branchChangesOut, error) {
		var out branchChangesOut
		dir, err := taskctx.Repo(root, in.Repo)
		if err != nil {
			return nil, out, err
		}
		branch := in.Branch
		if branch == "" {
			branch, err = taskctx.CurrentBranch(dir)
			if err != nil {
				return nil, out, err
			}
		}
		base, files, err := taskctx.ChangedFiles(dir, branch)
		if err != nil {
			return nil, out, err
		}
		out.Branch = branch
		out.Base = base
		const maxFiles = 40
		const perFile = 8
		if len(files) > maxFiles {
			files = files[:maxFiles]
			out.Truncated = true
		}
		out.Files = make([]branchFile, 0, len(files))
		for _, f := range files {
			entry := branchFile{File: f, Symbols: []query.SymbolHit{}}
			if q != nil {
				lookup := f
				if in.Repo != "" {
					lookup = strings.Trim(filepath.ToSlash(in.Repo), "/") + "/" + f
				}
				if hits, err := q.FileOutline(lookup); err == nil && len(hits) > 0 {
					if len(hits) > perFile {
						hits = hits[:perFile]
						out.Truncated = true
					}
					entry.Symbols = hits
				}
			}
			out.Files = append(out.Files, entry)
		}
		return nil, out, nil
	})

	type findTaskIn struct {
		ID string `json:"id" jsonschema:"task id as it appears in branch names, for example PAY-123"`
	}
	type findTaskHit struct {
		Repo    string `json:"repo,omitempty"`
		Branch  string `json:"branch"`
		HasNote bool   `json:"hasNote"`
		Preview string `json:"preview,omitempty"`
	}
	type findTaskOut struct {
		Results []findTaskHit `json:"results"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "find_task",
		Description: "Find git branches whose names match a task id, across the project and nested repositories. " +
			"Each repository may set task.branch in .kartograf.yml (a template containing {id}); otherwise the branch name only has to contain the id. " +
			"Returns the repository, branch, whether a handoff note exists, and its first line. " +
			"Use this when the user names a task instead of a branch, then get_task_context or branch_changes on the match.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in findTaskIn) (*mcp.CallToolResult, findTaskOut, error) {
		hits, err := taskctx.FindTask(root, in.ID)
		if err != nil {
			return nil, findTaskOut{}, err
		}
		out := make([]findTaskHit, 0, len(hits))
		for _, h := range hits {
			out = append(out, findTaskHit{Repo: h.Repo, Branch: h.Branch, HasNote: h.HasNote, Preview: h.Preview})
		}
		return nil, findTaskOut{Results: out}, nil
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

func taskContextNote(root string, note taskctx.Note) taskContextOut {
	return taskContextOut{
		Branch:     note.Branch,
		UpdatedAt:  formatUpdated(note.UpdatedAt),
		Body:       note.Body,
		StaleFiles: taskctx.StaleFiles(root, note),
	}
}

func formatUpdated(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
