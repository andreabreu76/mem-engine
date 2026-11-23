package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const (
	defaultContext = 5
	maxContext     = 20
	maxGetIDs      = 20
	getMaxBytes    = 20000
)

type Deps struct {
	Store   *store.Store
	Memory  *memory.Service
	Current project.Project
	Now     func() time.Time
	Log     *slog.Logger
	Version string
}

type searchIn struct {
	Query   string `json:"query" jsonschema:"Words to look for. Empty lists the most recent items."`
	Type    string `json:"type,omitempty" jsonschema:"Only items of this type, e.g. decision, gotcha, fact, or an observation kind."`
	Since   string `json:"since,omitempty" jsonschema:"Only items created since YYYY-MM-DD or in the last <N>d, e.g. 7d."`
	Project string `json:"project,omitempty" jsonschema:"Name or key of another project. Omit it unless the user explicitly asks about another project: by default only the current project and user-wide memories are visible."`
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum number of lines, default 10, at most 50."`
}

type timelineIn struct {
	ID      string `json:"id" jsonschema:"An id returned by search."`
	Before  *int   `json:"before,omitempty" jsonschema:"Items to show before the id, default 5, at most 20."`
	After   *int   `json:"after,omitempty" jsonschema:"Items to show after the id, default 5, at most 20."`
	Project string `json:"project,omitempty" jsonschema:"Name or key of another project. Omit it unless the user explicitly asks about another project: by default only the current project and user-wide memories are visible."`
}

type getIn struct {
	IDs     []string `json:"ids" jsonschema:"Ids chosen from search or timeline, at most 20."`
	Project string   `json:"project,omitempty" jsonschema:"Name or key of another project. Omit it unless the user explicitly asks about another project: by default only the current project and user-wide memories are visible."`
}

type saveIn struct {
	Type    string   `json:"type" jsonschema:"One of fact, decision, gotcha, preference, reference (lowercase letters, digits and dashes)."`
	Title   string   `json:"title" jsonschema:"Short title, one line."`
	Summary string   `json:"summary" jsonschema:"One sentence shown in search results."`
	Body    string   `json:"body" jsonschema:"Full content in Markdown, may be empty."`
	Tags    []string `json:"tags,omitempty"`
	Scope   string   `json:"scope,omitempty" jsonschema:"project (default) for the current project, user for every project."`
}

type updateIn struct {
	ID      string    `json:"id" jsonschema:"Id of a memory saved earlier."`
	Type    *string   `json:"type,omitempty"`
	Title   *string   `json:"title,omitempty"`
	Summary *string   `json:"summary,omitempty"`
	Body    *string   `json:"body,omitempty"`
	Tags    *[]string `json:"tags,omitempty" jsonschema:"Replaces the previous tags."`
}

type forgetIn struct {
	ID string `json:"id" jsonschema:"Id of a memory saved earlier."`
}

type server struct {
	Deps
}

func New(d Deps) *mcp.Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	s := &server{Deps: d}
	srv := mcp.NewServer(&mcp.Implementation{Name: "mem-engine", Version: d.Version}, &mcp.ServerOptions{Logger: d.Log})
	mcp.AddTool(srv, &mcp.Tool{Name: "search", Description: "Step 1 of 3: search long-term memory, curated memories and observations captured in past sessions, of the current project plus user-wide memories. " +
		"Returns one short line per item: <id> <date> <type> <title> | <summary>. " +
		"Next, call timeline on an interesting id for context, then get only for the ids you need in full."}, handle(s, "search", s.search))
	mcp.AddTool(srv, &mcp.Tool{Name: "timeline", Description: "Step 2 of 3: show what happened around an id from search, the items of the same session before and after it, in the search line format with the id marked by >. " +
		"Next, call get only for the ids you need in full."}, handle(s, "timeline", s.timeline))
	mcp.AddTool(srv, &mcp.Tool{Name: "get", Description: "Step 3 of 3: fetch the full content, header and body, of the ids chosen from search or timeline. " +
		"The whole response is limited to 20000 bytes and ids that do not fit are listed at the end, so ask only for what you need."}, handle(s, "get", s.get))
	mcp.AddTool(srv, &mcp.Tool{Name: "save", Description: "Save a curated memory that future sessions should know: a durable fact, decision, gotcha, preference or reference. " +
		"Use this instead of writing memory files such as MEMORY.md. Recommended types: fact, decision, gotcha, preference, reference. " +
		"scope project (default) ties it to the current project, user makes it visible in every project. Returns the new id."}, handle(s, "save", s.save))
	mcp.AddTool(srv, &mcp.Tool{Name: "update", Description: "Change a memory saved earlier, by id. Only the fields given change; tags replace the previous list."}, handle(s, "update", s.update))
	mcp.AddTool(srv, &mcp.Tool{Name: "forget", Description: "Delete a memory saved earlier, by id. Observations captured from sessions cannot be deleted."}, handle(s, "forget", s.forget))
	return srv
}

func handle[In any](s *server, name string, h func(context.Context, In) (string, error)) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		text, err := h(ctx, in)
		if err != nil {
			s.Log.Warn("mcp tool failed", "tool", name, "err", err)
			return nil, nil, toolError(name, err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	}
}

func (s *server) scope(ctx context.Context, name string) (store.Scope, error) {
	if name == "" {
		return store.CurrentScope(s.Current.Key), nil
	}
	row, err := s.Store.FindProject(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Scope{}, userErrorf("unknown project %q", name)
	}
	if err != nil && strings.Contains(err.Error(), " is ambiguous") {
		return store.Scope{}, userError(err.Error())
	}
	if err != nil {
		return store.Scope{}, err
	}
	return store.CurrentScope(row.Key), nil
}

func (s *server) search(ctx context.Context, in searchIn) (string, error) {
	since, err := parseSince(in.Since, s.Now())
	if err != nil {
		return "", err
	}
	sc, err := s.scope(ctx, in.Project)
	if err != nil {
		return "", err
	}
	hits, err := s.Store.Search(ctx, store.SearchParams{Query: in.Query, Type: in.Type, Since: since, Limit: in.Limit, Scope: sc})
	if err != nil {
		return "", err
	}
	return formatHits(hits), nil
}

func (s *server) timeline(ctx context.Context, in timelineIn) (string, error) {
	sc, err := s.scope(ctx, in.Project)
	if err != nil {
		return "", err
	}
	hits, err := s.Store.Timeline(ctx, in.ID, contextSize(in.Before), contextSize(in.After), sc)
	if errors.Is(err, store.ErrNotFound) {
		return "", notFound(in.ID)
	}
	if err != nil {
		return "", err
	}
	return formatTimeline(hits, in.ID), nil
}

func contextSize(n *int) int {
	if n == nil {
		return defaultContext
	}
	return min(max(*n, 0), maxContext)
}

func (s *server) get(ctx context.Context, in getIn) (string, error) {
	if len(in.IDs) == 0 {
		return "", userError("ids is empty")
	}
	if len(in.IDs) > maxGetIDs {
		return "", userErrorf("at most %d ids per call", maxGetIDs)
	}
	sc, err := s.scope(ctx, in.Project)
	if err != nil {
		return "", err
	}
	items, err := s.Store.Get(ctx, in.IDs, sc, getMaxBytes)
	if err != nil {
		return "", err
	}
	var missing []string
	for _, id := range in.IDs {
		found := slices.ContainsFunc(items, func(it store.Item) bool { return it.ID == id })
		if !found && !slices.Contains(missing, id) {
			missing = append(missing, id)
		}
	}
	return formatItems(items, missing, getMaxBytes), nil
}

func (s *server) save(ctx context.Context, in saveIn) (string, error) {
	p := s.Current
	switch in.Scope {
	case "", "project":
	case "user":
		p = project.User
	default:
		return "", userErrorf("invalid scope %q: want project or user", in.Scope)
	}
	m, err := s.Memory.Save(ctx, p, memory.Input{Type: in.Type, Title: in.Title, Summary: in.Summary, Body: in.Body, Tags: in.Tags})
	if err != nil {
		return "", err
	}
	return "saved " + m.ID + "\n", nil
}

func (s *server) update(ctx context.Context, in updateIn) (string, error) {
	if err := s.ownMemory(ctx, in.ID); err != nil {
		return "", err
	}
	patch := memory.Patch{Type: in.Type, Title: in.Title, Summary: in.Summary, Body: in.Body, Tags: in.Tags}
	if patch == (memory.Patch{}) {
		return "", userError("nothing to update: give at least one of type, title, summary, body, tags")
	}
	if _, err := s.Memory.Update(ctx, in.ID, patch); err != nil {
		return "", err
	}
	return "updated " + in.ID + "\n", nil
}

func (s *server) forget(ctx context.Context, in forgetIn) (string, error) {
	if err := s.ownMemory(ctx, in.ID); err != nil {
		return "", err
	}
	if err := s.Memory.Forget(ctx, in.ID); err != nil {
		return "", err
	}
	return "forgot " + in.ID + "\n", nil
}

func (s *server) ownMemory(ctx context.Context, id string) error {
	m, err := s.Store.GetMemory(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !slices.Contains(store.CurrentScope(s.Current.Key).Projects, m.Project)) {
		return notFound(id)
	}
	return err
}

func notFound(id string) error {
	return userErrorf("%s not found", id)
}
