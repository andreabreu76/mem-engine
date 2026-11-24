package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	kindMemory          = "memory"
	kindObservation     = "observation"
	defaultSearchLimit  = 10
	maxSearchLimit      = 50
	ftsCandidatesFactor = 3
	maxQueryTokens      = 32
	defaultGetMaxBytes  = 20000
	recencyHalfDays     = 30.0
	summaryMaxChars     = 100
)

var queryToken = regexp.MustCompile(`[\p{L}\p{M}\p{N}_]+`)

type Scope struct{ Projects []string }

func CurrentScope(projectKey string) Scope {
	return Scope{Projects: []string{projectKey, "user"}}
}

type Hit struct {
	ID, Kind, Type, Title, Summary, Project, SessionID string
	At                                                 time.Time
	Score                                              float64
}

type Item struct {
	Hit
	Body      string
	Tags      []string
	ToolName  string
	Files     []string
	Truncated bool
}

type SearchParams struct {
	Query, Type string
	Since       time.Time
	Limit       int
	Scope       Scope
}

type corpus struct {
	kind, table, fts, join, cols, typeCol string
	weight                                float64
}

var memoryCorpus = corpus{
	kind:    kindMemory,
	table:   "memories t",
	fts:     "memories_fts",
	join:    "memories_fts.rowid = t.rowid",
	cols:    "t.id, t.type, t.title, t.summary, t.project, coalesce(t.source_session, ''), t.created_at",
	typeCol: "t.type",
	weight:  2.0,
}

var observationCorpus = corpus{
	kind:    kindObservation,
	table:   "observations t",
	fts:     "observations_fts",
	join:    "observations_fts.rowid = t.id",
	cols:    "'o-' || t.id, t.kind, t.title, substr(t.body, 1, 100), t.project, t.session_id, t.created_at",
	typeCol: "t.kind",
	weight:  1.0,
}

func FTSQuery(q string) string {
	tokens := queryToken.FindAllString(q, maxQueryTokens)
	for i, t := range tokens {
		tokens[i] = `"` + t + `"`
	}
	return strings.Join(tokens, " OR ")
}

func (s *Store) Search(ctx context.Context, p SearchParams) ([]Hit, error) {
	if len(p.Scope.Projects) == 0 {
		return nil, nil
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	limit = min(limit, maxSearchLimit)
	fts := FTSQuery(p.Query)
	perCorpus := limit
	if fts != "" {
		perCorpus = ftsCandidatesFactor * limit
	}
	now := s.clock()
	var hits []Hit
	for _, c := range []corpus{memoryCorpus, observationCorpus} {
		found, err := s.searchCorpus(ctx, c, fts, p, perCorpus, now)
		if err != nil {
			return nil, fmt.Errorf("search %s: %w", c.fts, err)
		}
		hits = append(hits, found...)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].At.After(hits[j].At)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *Store) searchCorpus(ctx context.Context, c corpus, fts string, p SearchParams, limit int, now time.Time) ([]Hit, error) {
	where, args := scopeClause("t.project", p.Scope)
	if p.Type != "" {
		where += " AND " + c.typeCol + " = ?"
		args = append(args, p.Type)
	}
	if !p.Since.IsZero() {
		where += " AND t.created_at >= ?"
		args = append(args, p.Since.Unix())
	}
	var q string
	if fts == "" {
		q = "SELECT " + c.cols + ", 0 FROM " + c.table + " WHERE " + where + " ORDER BY t.created_at DESC, t.rowid DESC LIMIT ?"
	} else {
		q = "SELECT " + c.cols + ", bm25(" + c.fts + ") FROM " + c.fts + " JOIN " + c.table + " ON " + c.join +
			" WHERE " + c.fts + " MATCH ? AND " + where + " ORDER BY bm25(" + c.fts + ") LIMIT ?"
		args = append([]any{fts}, args...)
	}
	args = append(args, limit)
	hits, err := s.queryHits(ctx, c.kind, q, args...)
	if err != nil {
		return nil, err
	}
	if fts == "" {
		return hits, nil
	}
	best := 0.0
	for _, h := range hits {
		best = max(best, -h.Score)
	}
	for i := range hits {
		relevance := 0.0
		if best > 0 {
			relevance = -hits[i].Score / best
		}
		hits[i].Score = relevance * c.weight * recency(now, hits[i].At)
	}
	return hits, nil
}

func (s *Store) queryHits(ctx context.Context, kind, q string, args ...any) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		h := Hit{Kind: kind}
		var created int64
		if err := rows.Scan(&h.ID, &h.Type, &h.Title, &h.Summary, &h.Project, &h.SessionID, &created, &h.Score); err != nil {
			return nil, err
		}
		h.At = time.Unix(created, 0).UTC()
		if kind == kindObservation {
			h.Summary = observationSummary(h.Summary)
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (s *Store) Timeline(ctx context.Context, id string, before, after int, sc Scope) ([]Hit, error) {
	anchor, found, err := s.item(ctx, id, sc)
	if err != nil {
		return nil, fmt.Errorf("timeline %s: %w", id, err)
	}
	if !found {
		return nil, fmt.Errorf("timeline %s: %w", id, ErrNotFound)
	}
	if anchor.SessionID == "" {
		return []Hit{anchor.Hit}, nil
	}
	at := anchor.At.Unix()
	olderCond, olderArgs := "t.created_at <= ?", []any{at}
	newerCond, newerArgs := "t.created_at > ?", []any{at}
	if anchor.Kind == kindObservation {
		rowid, _ := strconv.ParseInt(strings.TrimPrefix(anchor.ID, "o-"), 10, 64)
		olderCond, olderArgs = "(t.created_at < ? OR (t.created_at = ? AND t.id < ?))", []any{at, at, rowid}
		newerCond, newerArgs = "(t.created_at > ? OR (t.created_at = ? AND t.id > ?))", []any{at, at, rowid}
	}
	older, err := s.sessionNeighbors(ctx, anchor.SessionID, sc, olderCond, olderArgs, "DESC", before)
	if err != nil {
		return nil, fmt.Errorf("timeline %s: %w", id, err)
	}
	newer, err := s.sessionNeighbors(ctx, anchor.SessionID, sc, newerCond, newerArgs, "ASC", after)
	if err != nil {
		return nil, fmt.Errorf("timeline %s: %w", id, err)
	}
	hits := make([]Hit, 0, len(older)+1+len(newer))
	for i := len(older) - 1; i >= 0; i-- {
		hits = append(hits, older[i])
	}
	hits = append(hits, anchor.Hit)
	return append(hits, newer...), nil
}

func (s *Store) sessionNeighbors(ctx context.Context, session string, sc Scope, cond string, condArgs []any, order string, limit int) ([]Hit, error) {
	if limit <= 0 {
		return nil, nil
	}
	where, args := scopeClause("t.project", sc)
	args = append([]any{session}, args...)
	args = append(append(args, condArgs...), limit)
	q := "SELECT " + observationCorpus.cols + ", 0 FROM observations t WHERE t.session_id = ? AND " + where +
		" AND " + cond + " ORDER BY t.created_at " + order + ", t.id " + order + " LIMIT ?"
	return s.queryHits(ctx, kindObservation, q, args...)
}

func (s *Store) Get(ctx context.Context, ids []string, sc Scope, maxBytes int) ([]Item, error) {
	if maxBytes <= 0 {
		maxBytes = defaultGetMaxBytes
	}
	seen := map[string]bool{}
	var items []Item
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		it, found, err := s.item(ctx, id, sc)
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", id, err)
		}
		if found {
			items = append(items, it)
		}
	}
	remaining := maxBytes
	for i := range items {
		if len(items[i].Body) > remaining {
			items[i].Body = cutUTF8(items[i].Body, remaining)
			items[i].Truncated = true
		}
		remaining -= len(items[i].Body)
	}
	return items, nil
}

func (s *Store) item(ctx context.Context, id string, sc Scope) (Item, bool, error) {
	if len(sc.Projects) == 0 {
		return Item{}, false, nil
	}
	where, args := scopeClause("project", sc)
	if rest, ok := strings.CutPrefix(id, "m-"); ok && rest != "" {
		return s.memoryItem(ctx, id, where, args)
	}
	if rest, ok := strings.CutPrefix(id, "o-"); ok {
		rowid, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || rowid <= 0 || "o-"+strconv.FormatInt(rowid, 10) != id {
			return Item{}, false, nil
		}
		return s.observationItem(ctx, rowid, where, args)
	}
	return Item{}, false, nil
}

func (s *Store) memoryItem(ctx context.Context, id, where string, scopeArgs []any) (Item, bool, error) {
	it := Item{Hit: Hit{Kind: kindMemory}}
	var created int64
	var tags string
	err := s.db.QueryRowContext(ctx, `
SELECT id, type, title, summary, project, coalesce(source_session, ''), created_at, body, tags
FROM memories WHERE id = ? AND `+where, append([]any{id}, scopeArgs...)...).Scan(
		&it.ID, &it.Type, &it.Title, &it.Summary, &it.Project, &it.SessionID, &created, &it.Body, &tags)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	if err := json.Unmarshal([]byte(tags), &it.Tags); err != nil {
		return Item{}, false, fmt.Errorf("decode tags: %w", err)
	}
	it.At = time.Unix(created, 0).UTC()
	return it, true, nil
}

func (s *Store) observationItem(ctx context.Context, rowid int64, where string, scopeArgs []any) (Item, bool, error) {
	it := Item{Hit: Hit{Kind: kindObservation, ID: "o-" + strconv.FormatInt(rowid, 10)}}
	var created int64
	var files sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT kind, title, project, session_id, created_at, body, coalesce(tool_name, ''), files
FROM observations WHERE id = ? AND `+where, append([]any{rowid}, scopeArgs...)...).Scan(
		&it.Type, &it.Title, &it.Project, &it.SessionID, &created, &it.Body, &it.ToolName, &files)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	if files.String != "" {
		if err := json.Unmarshal([]byte(files.String), &it.Files); err != nil {
			return Item{}, false, fmt.Errorf("decode files: %w", err)
		}
	}
	it.At = time.Unix(created, 0).UTC()
	it.Summary = observationSummary(it.Body)
	return it, true, nil
}

func scopeClause(column string, sc Scope) (string, []any) {
	args := make([]any, len(sc.Projects))
	for i, p := range sc.Projects {
		args[i] = p
	}
	return column + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")", args
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func recency(now, at time.Time) float64 {
	ageDays := max(now.Sub(at).Hours()/24, 0)
	return 1 / (1 + ageDays/recencyHalfDays)
}

func observationSummary(body string) string {
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	body = strings.TrimRight(body, "\r")
	if utf8.RuneCountInString(body) <= summaryMaxChars {
		return body
	}
	return string([]rune(body)[:summaryMaxChars])
}

func cutUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
