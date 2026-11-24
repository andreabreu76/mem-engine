package ui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/spool"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const (
	allProjects     = "*"
	feedLimit       = 200
	searchLimit     = 50
	itemMaxBytes    = 1 << 20
	timelineContext = 5
)

type projectJSON struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

type hitJSON struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Project   string    `json:"project"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	Score     float64   `json:"score"`
}

type itemJSON struct {
	hitJSON
	Body      string   `json:"body"`
	Tags      []string `json:"tags"`
	ToolName  string   `json:"tool_name"`
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

type itemResponse struct {
	Item     itemJSON  `json:"item"`
	Timeline []hitJSON `json:"timeline"`
}

type memoryJSON struct {
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Type    string    `json:"type"`
	Title   string    `json:"title"`
	Summary string    `json:"summary"`
	Body    string    `json:"body"`
	Tags    []string  `json:"tags"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

type patchJSON struct {
	Type    *string   `json:"type"`
	Title   *string   `json:"title"`
	Summary *string   `json:"summary"`
	Body    *string   `json:"body"`
	Tags    *[]string `json:"tags"`
}

type deletedJSON struct {
	Deleted string `json:"deleted"`
}

type healthJSON struct {
	SpoolPending     int    `json:"spool_pending"`
	CaptureErrors24h int    `json:"capture_errors_24h"`
	LastError        string `json:"last_error"`
}

func (s *Server) scope(r *http.Request) (store.Scope, error) {
	name := r.URL.Query().Get("project")
	switch name {
	case "":
		return store.Scope{}, badRequest("project is required: a project name or key, or * for all projects")
	case allProjects:
		rows, err := s.Store.ListProjects(r.Context())
		if err != nil {
			return store.Scope{}, err
		}
		keys := []string{"user"}
		for _, p := range rows {
			if p.Key != "user" {
				keys = append(keys, p.Key)
			}
		}
		return store.Scope{Projects: keys}, nil
	}
	row, err := s.Store.FindProject(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Scope{}, badRequest("unknown project")
	}
	if errors.Is(err, store.ErrAmbiguous) {
		return store.Scope{}, badRequest(err.Error())
	}
	if err != nil {
		return store.Scope{}, err
	}
	return store.CurrentScope(row.Key), nil
}

func (s *Server) projects(_ http.ResponseWriter, r *http.Request) (any, error) {
	rows, err := s.Store.ListProjects(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]projectJSON, len(rows))
	for i, p := range rows {
		out[i] = projectJSON{Key: p.Key, Name: p.Name, Dir: p.Dir}
	}
	return out, nil
}

func (s *Server) feed(_ http.ResponseWriter, r *http.Request) (any, error) {
	sc, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil || after < 0 {
			return nil, badRequest("after must be a non-negative observation number")
		}
	}
	hits, err := s.Store.Feed(r.Context(), after, sc.Projects, feedLimit)
	if err != nil {
		return nil, err
	}
	return toHits(hits), nil
}

func (s *Server) search(_ http.ResponseWriter, r *http.Request) (any, error) {
	sc, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		since, err = time.Parse(time.DateOnly, v)
		if err != nil {
			return nil, badRequest("since must be YYYY-MM-DD")
		}
	}
	hits, err := s.Store.Search(r.Context(), store.SearchParams{
		Query: q.Get("q"), Type: q.Get("type"), Since: since, Limit: searchLimit, Scope: sc,
	})
	if err != nil {
		return nil, err
	}
	return toHits(hits), nil
}

func (s *Server) item(_ http.ResponseWriter, r *http.Request) (any, error) {
	sc, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	items, err := s.Store.Get(r.Context(), []string{id}, sc, itemMaxBytes)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errNotFound
	}
	timeline, err := s.Store.Timeline(r.Context(), id, timelineContext, timelineContext, sc)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return itemResponse{Item: toItem(items[0]), Timeline: toHits(timeline)}, nil
}

func (s *Server) updateMemory(_ http.ResponseWriter, r *http.Request) (any, error) {
	sc, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	var in patchJSON
	if err := decodeBody(r.Body, &in); err != nil {
		return nil, err
	}
	patch := memory.Patch{Type: in.Type, Title: in.Title, Summary: in.Summary, Body: in.Body, Tags: in.Tags}
	if patch == (memory.Patch{}) {
		return nil, badRequest("nothing to update: give at least one of type, title, summary, body, tags")
	}
	id := r.PathValue("id")
	if err := s.ownMemory(r.Context(), sc, id); err != nil {
		return nil, err
	}
	m, err := s.Memory.Update(r.Context(), id, patch)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound
	}
	if errors.Is(err, memory.ErrInvalid) {
		return nil, badRequest(err.Error())
	}
	if err != nil {
		return nil, err
	}
	return toMemory(m), nil
}

func (s *Server) deleteMemory(_ http.ResponseWriter, r *http.Request) (any, error) {
	sc, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	if err := s.ownMemory(r.Context(), sc, id); err != nil {
		return nil, err
	}
	if err := s.Memory.Forget(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound
	} else if err != nil {
		return nil, err
	}
	return deletedJSON{Deleted: id}, nil
}

func (s *Server) deleteObservation(_ http.ResponseWriter, r *http.Request) (any, error) {
	sc, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	rest, ok := strings.CutPrefix(id, "o-")
	rowid, err := strconv.ParseInt(rest, 10, 64)
	if !ok || err != nil || rowid <= 0 || "o-"+strconv.FormatInt(rowid, 10) != id {
		return nil, errNotFound
	}
	items, err := s.Store.Get(r.Context(), []string{id}, sc, 1)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errNotFound
	}
	if err := s.Store.DeleteObservation(r.Context(), rowid); errors.Is(err, store.ErrNotFound) {
		return nil, errNotFound
	} else if err != nil {
		return nil, err
	}
	return deletedJSON{Deleted: id}, nil
}

func (s *Server) health(_ http.ResponseWriter, r *http.Request) (any, error) {
	pending, err := spool.Pending(s.Cfg.SpoolDir())
	if err != nil {
		return nil, err
	}
	count, last, err := s.Store.CaptureErrorsSince(r.Context(), time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	return healthJSON{SpoolPending: pending, CaptureErrors24h: count, LastError: last}, nil
}

func (s *Server) ownMemory(ctx context.Context, sc store.Scope, id string) error {
	m, err := s.Store.GetMemory(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !slices.Contains(sc.Projects, m.Project)) {
		return errNotFound
	}
	return err
}

func decodeBody(body io.Reader, v any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return jsonError(err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return jsonError(err)
		}
		return badRequest("body must be a single JSON object")
	}
	return nil
}

func jsonError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return err
	}
	return badRequest("invalid JSON body: " + err.Error())
}

func toHits(hits []store.Hit) []hitJSON {
	out := make([]hitJSON, len(hits))
	for i, h := range hits {
		out[i] = toHit(h)
	}
	return out
}

func toHit(h store.Hit) hitJSON {
	return hitJSON{ID: h.ID, Kind: h.Kind, Type: h.Type, Title: h.Title, Summary: h.Summary,
		Project: h.Project, SessionID: h.SessionID, At: h.At, Score: h.Score}
}

func toItem(it store.Item) itemJSON {
	return itemJSON{hitJSON: toHit(it.Hit), Body: it.Body, Tags: nonNil(it.Tags), ToolName: it.ToolName,
		Files: nonNil(it.Files), Truncated: it.Truncated}
}

func toMemory(m store.Memory) memoryJSON {
	return memoryJSON{ID: m.ID, Project: m.Project, Type: m.Type, Title: m.Title, Summary: m.Summary,
		Body: m.Body, Tags: nonNil(m.Tags), Created: m.Created, Updated: m.Updated}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
