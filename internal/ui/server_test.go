package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/spool"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const testPort = 47600

var (
	billing = project.Project{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: project.DirName("billing-api", "/home/dev/src/billing-api")}
	ledger  = project.Project{Key: "/home/dev/src/ledger", Name: "ledger", Dir: project.DirName("ledger", "/home/dev/src/ledger")}
	testNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
)

type fixture struct {
	srv *Server
	h   http.Handler
	log *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("MEM_ENGINE_HOME", home)
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cfg, err := config.LoadFrom(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := &memory.Service{Store: st, WikiRoot: cfg.WikiDir(), Now: func() time.Time { return testNow }}
	ctx := context.Background()
	for _, p := range []project.Project{billing, ledger} {
		if err := st.EnsureProject(ctx, store.ProjectRow{Key: p.Key, Name: p.Name, Dir: p.Dir}); err != nil {
			t.Fatal(err)
		}
	}
	log := &bytes.Buffer{}
	srv := &Server{Store: st, Memory: svc, Cfg: cfg, Token: NewToken(), Port: testPort, Log: slog.New(slog.NewTextHandler(log, nil))}
	return fixture{srv: srv, h: srv.Handler(), log: log}
}

func (f fixture) observe(t *testing.T, session, projectKey, title, body string, at time.Time) string {
	t.Helper()
	id, err := f.srv.Store.InsertObservation(context.Background(), store.Observation{
		SessionID: session, Project: projectKey, Kind: "tool", Title: title, Body: body, ToolName: "Bash", CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return "o-" + strconv.FormatInt(id, 10)
}

func (f fixture) save(t *testing.T, p project.Project, title string) string {
	t.Helper()
	m, err := f.srv.Memory.Save(context.Background(), p, memory.Input{Type: "decision", Title: title, Summary: "summary of " + title, Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func (f fixture) request(method, target, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.Host = "127.0.0.1:" + strconv.Itoa(testPort)
	r.Header.Set("X-Mem-Token", f.srv.Token)
	if method != http.MethodGet {
		r.Header.Set("Origin", "http://127.0.0.1:"+strconv.Itoa(testPort))
	}
	if method == http.MethodPut {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func (f fixture) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f fixture) do(t *testing.T, method, target, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := f.serve(f.request(method, target, body))
	if w.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, target, w.Code, w.Body.String(), want)
	}
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func ids(hits []hitJSON) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out
}

func TestRejectsBadHost(t *testing.T) {
	f := newFixture(t)
	for _, host := range []string{"evil.example:47600", "127.0.0.1:1", "127.0.0.1", "localhost.evil.example:47600"} {
		r := f.request(http.MethodGet, "/api/projects", "")
		r.Host = host
		if w := f.serve(r); w.Code != http.StatusForbidden {
			t.Errorf("Host %s = %d, want 403", host, w.Code)
		}
	}
	r := f.request(http.MethodGet, "/api/projects", "")
	r.Host = "evil.example:47600"
	r.Header.Del("X-Mem-Token")
	if w := f.serve(r); w.Code != http.StatusForbidden {
		t.Errorf("bad Host without token = %d, want 403 before the token check", w.Code)
	}
	r = f.request(http.MethodGet, "/api/projects", "")
	r.Host = "localhost:47600"
	if w := f.serve(r); w.Code != http.StatusOK {
		t.Errorf("Host localhost:47600 = %d, want 200", w.Code)
	}
}

func TestRejectsMissingToken(t *testing.T) {
	f := newFixture(t)
	r := f.request(http.MethodGet, "/api/projects", "")
	r.Header.Del("X-Mem-Token")
	if w := f.serve(r); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", w.Code)
	}
	for _, bad := range []string{strings.Repeat("0", len(f.srv.Token)), f.srv.Token[:10], f.srv.Token + "0"} {
		r = f.request(http.MethodGet, "/api/projects", "")
		r.Header.Set("X-Mem-Token", bad)
		if w := f.serve(r); w.Code != http.StatusUnauthorized {
			t.Errorf("wrong token %q = %d, want 401", bad, w.Code)
		}
	}
	r = f.request(http.MethodGet, "/api/projects?token="+f.srv.Token, "")
	r.Header.Del("X-Mem-Token")
	if w := f.serve(r); w.Code != http.StatusOK {
		t.Errorf("token query param = %d, want 200", w.Code)
	}
}

func TestEmptyServerTokenRejectsEverything(t *testing.T) {
	f := newFixture(t)
	f.srv.Token = ""
	h := f.srv.Handler()
	r := f.request(http.MethodGet, "/api/projects", "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty server token = %d, want 401", w.Code)
	}
}

func TestNewTokenIsRandomHex(t *testing.T) {
	a, b := NewToken(), NewToken()
	if len(a) != 64 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("NewToken = %q, want 64 hex characters", a)
	}
	if a == b {
		t.Fatal("NewToken repeated a value")
	}
}

func TestRejectsCrossOriginWrite(t *testing.T) {
	f := newFixture(t)
	id := f.save(t, billing, "keep me")
	target := "/api/memory/" + id + "?project=" + billing.Name

	r := f.request(http.MethodDelete, target, "")
	r.Header.Set("Origin", "http://evil.example")
	if w := f.serve(r); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin DELETE = %d, want 403", w.Code)
	}
	r = f.request(http.MethodDelete, target, "")
	r.Header.Del("Origin")
	if w := f.serve(r); w.Code != http.StatusForbidden {
		t.Fatalf("DELETE without Origin = %d, want 403", w.Code)
	}
	r = f.request(http.MethodPut, target, `{"title":"hijacked"}`)
	r.Header.Set("Origin", "http://localhost:1")
	if w := f.serve(r); w.Code != http.StatusForbidden {
		t.Fatalf("PUT from other port = %d, want 403", w.Code)
	}
	r = f.request(http.MethodPut, target, `{"title":"hijacked"}`)
	r.Header.Set("Content-Type", "text/plain")
	if w := f.serve(r); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("PUT text/plain = %d, want 415", w.Code)
	}
	r = f.request("PROPFIND", target, "")
	r.Header.Del("Origin")
	if w := f.serve(r); w.Code != http.StatusForbidden {
		t.Fatalf("PROPFIND without Origin = %d, want 403", w.Code)
	}
	m, err := f.srv.Store.GetMemory(context.Background(), id)
	if err != nil || m.Title != "keep me" {
		t.Fatalf("memory after rejected writes = %+v, %v", m, err)
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	ok := f.do(t, http.MethodGet, "/api/projects", "", http.StatusOK)
	r := f.request(http.MethodGet, "/api/projects", "")
	r.Header.Del("X-Mem-Token")
	r.Header.Set("Origin", "http://evil.example")
	denied := f.serve(r)
	for _, w := range []*httptest.ResponseRecorder{ok, denied} {
		h := w.Result().Header
		for k, want := range map[string]string{
			"Content-Type":            "application/json; charset=utf-8",
			"X-Content-Type-Options":  "nosniff",
			"Cache-Control":           "no-store",
			"Referrer-Policy":         "no-referrer",
			"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'",
		} {
			if got := h.Get(k); got != want {
				t.Errorf("status %d header %s = %q, want %q", w.Code, k, got, want)
			}
		}
		for k := range h {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("status %d has CORS header %s", w.Code, k)
			}
		}
	}
}

func TestProjects(t *testing.T) {
	f := newFixture(t)
	got := decode[[]projectJSON](t, f.do(t, http.MethodGet, "/api/projects", "", http.StatusOK))
	want := []projectJSON{
		{Key: billing.Key, Name: billing.Name, Dir: billing.Dir},
		{Key: ledger.Key, Name: ledger.Name, Dir: ledger.Dir},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projects = %+v, want %+v", got, want)
	}
}

func TestFeedAfterID(t *testing.T) {
	f := newFixture(t)
	first := f.observe(t, "s1", billing.Key, "first", "one", testNow.Add(-3*time.Hour))
	second := f.observe(t, "s1", billing.Key, "second", "two", testNow.Add(-2*time.Hour))
	other := f.observe(t, "s2", ledger.Key, "other", "three", testNow.Add(-2*time.Hour))
	user := f.observe(t, "s3", "user", "user wide", "four", testNow.Add(-1*time.Hour))

	f.do(t, http.MethodGet, "/api/feed?after=0", "", http.StatusBadRequest)
	f.do(t, http.MethodGet, "/api/feed?after=x&project=*", "", http.StatusBadRequest)
	f.do(t, http.MethodGet, "/api/feed?after=-1&project=*", "", http.StatusBadRequest)
	f.do(t, http.MethodGet, "/api/feed?project=nope", "", http.StatusBadRequest)

	all := decode[[]hitJSON](t, f.do(t, http.MethodGet, "/api/feed?project=*", "", http.StatusOK))
	if got, want := ids(all), []string{first, second, other, user}; !reflect.DeepEqual(got, want) {
		t.Fatalf("feed all = %v, want %v", got, want)
	}
	after := strings.TrimPrefix(first, "o-")
	scoped := decode[[]hitJSON](t, f.do(t, http.MethodGet, "/api/feed?after="+after+"&project="+billing.Name, "", http.StatusOK))
	if got, want := ids(scoped), []string{second, user}; !reflect.DeepEqual(got, want) {
		t.Fatalf("feed billing after %s = %v, want %v", after, got, want)
	}
	if scoped[0].Title != "second" || scoped[0].Project != billing.Key || scoped[0].Kind != "observation" || scoped[0].Type != "tool" {
		t.Fatalf("feed hit = %+v", scoped[0])
	}
	last := strings.TrimPrefix(user, "o-")
	w := f.do(t, http.MethodGet, "/api/feed?after="+last+"&project=*", "", http.StatusOK)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty feed = %s, want []", w.Body.String())
	}
}

func TestSearchRequiresProjectAndStaysInScope(t *testing.T) {
	f := newFixture(t)
	f.save(t, billing, "invoice rounding")
	f.save(t, ledger, "invoice ledger")

	f.do(t, http.MethodGet, "/api/search?q=invoice", "", http.StatusBadRequest)
	f.do(t, http.MethodGet, "/api/search?q=invoice&project=*&since=yesterday", "", http.StatusBadRequest)
	hits := decode[[]hitJSON](t, f.do(t, http.MethodGet, "/api/search?q=invoice&project="+billing.Name, "", http.StatusOK))
	if len(hits) != 1 || hits[0].Title != "invoice rounding" {
		t.Fatalf("search billing = %+v", hits)
	}
	all := decode[[]hitJSON](t, f.do(t, http.MethodGet, "/api/search?q=invoice&project=*&type=decision&since=2026-11-01", "", http.StatusOK))
	if len(all) != 2 {
		t.Fatalf("search all = %+v, want 2 hits", all)
	}
}

func TestItemWithTimeline(t *testing.T) {
	f := newFixture(t)
	before := f.observe(t, "s1", billing.Key, "before", "zero", testNow.Add(-3*time.Hour))
	anchor := f.observe(t, "s1", billing.Key, "anchor", "full body\nsecond line", testNow.Add(-2*time.Hour))
	after := f.observe(t, "s1", billing.Key, "after", "two", testNow.Add(-1*time.Hour))

	f.do(t, http.MethodGet, "/api/item/"+anchor, "", http.StatusBadRequest)
	missing := f.do(t, http.MethodGet, "/api/item/"+anchor+"?project="+ledger.Name, "", http.StatusNotFound)
	unknown := f.do(t, http.MethodGet, "/api/item/o-999999?project="+billing.Name, "", http.StatusNotFound)
	if missing.Body.String() != unknown.Body.String() {
		t.Fatalf("out-of-scope body %q differs from unknown body %q", missing.Body.String(), unknown.Body.String())
	}

	got := decode[itemResponse](t, f.do(t, http.MethodGet, "/api/item/"+anchor+"?project="+billing.Name, "", http.StatusOK))
	if got.Item.ID != anchor || got.Item.Body != "full body\nsecond line" || got.Item.ToolName != "Bash" {
		t.Fatalf("item = %+v", got.Item)
	}
	if got, want := ids(got.Timeline), []string{before, anchor, after}; !reflect.DeepEqual(got, want) {
		t.Fatalf("timeline = %v, want %v", got, want)
	}
}

func TestUpdateMemoryCommits(t *testing.T) {
	f := newFixture(t)
	id := f.save(t, billing, "old title")
	target := "/api/memory/" + id + "?project=" + billing.Name

	f.do(t, http.MethodPut, "/api/memory/"+id+"?project="+ledger.Name, `{"title":"new title"}`, http.StatusNotFound)
	f.do(t, http.MethodPut, "/api/memory/"+id, `{"title":"new title"}`, http.StatusBadRequest)
	f.do(t, http.MethodPut, target, `{"title":"new title","owner":"x"}`, http.StatusBadRequest)
	f.do(t, http.MethodPut, target, `{}`, http.StatusBadRequest)
	f.do(t, http.MethodPut, target, `{"title":"x"} {"title":"y"}`, http.StatusBadRequest)
	f.do(t, http.MethodPut, target, `{"type":"Not Valid"}`, http.StatusBadRequest)
	f.do(t, http.MethodPut, target, `{"title":"`+strings.Repeat("a", 1<<20)+`"}`, http.StatusRequestEntityTooLarge)

	got := decode[memoryJSON](t, f.do(t, http.MethodPut, target, `{"title":"new title","tags":["pinned"]}`, http.StatusOK))
	if got.ID != id || got.Title != "new title" || !reflect.DeepEqual(got.Tags, []string{"pinned"}) {
		t.Fatalf("updated memory = %+v", got)
	}
	m, err := f.srv.Store.GetMemory(context.Background(), id)
	if err != nil || m.Title != "new title" {
		t.Fatalf("stored memory = %+v, %v", m, err)
	}
	out, err := exec.Command("git", "-C", filepath.Join(f.srv.Cfg.WikiDir(), billing.Dir), "log", "--format=%s").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.HasPrefix(string(out), "mem: update "+id+" new title\n") {
		t.Fatalf("git log = %q, want an update commit on top", out)
	}
}

func TestDeleteMemoryScoped(t *testing.T) {
	f := newFixture(t)
	id := f.save(t, billing, "to forget")
	f.do(t, http.MethodDelete, "/api/memory/"+id+"?project="+ledger.Name, "", http.StatusNotFound)
	f.do(t, http.MethodDelete, "/api/memory/"+id+"?project="+billing.Name, "", http.StatusOK)
	if _, err := f.srv.Store.GetMemory(context.Background(), id); err == nil {
		t.Fatal("memory still in the store after DELETE")
	}
	f.do(t, http.MethodDelete, "/api/memory/"+id+"?project="+billing.Name, "", http.StatusNotFound)
}

func TestDeleteObservation(t *testing.T) {
	f := newFixture(t)
	id := f.observe(t, "s1", billing.Key, "secret", "uniquebodyword", testNow)

	f.do(t, http.MethodDelete, "/api/observation/"+id, "", http.StatusBadRequest)
	f.do(t, http.MethodDelete, "/api/observation/"+id+"?project="+ledger.Name, "", http.StatusNotFound)
	f.do(t, http.MethodDelete, "/api/observation/o-01?project=*", "", http.StatusNotFound)
	f.do(t, http.MethodDelete, "/api/observation/"+id+"?project="+billing.Name, "", http.StatusOK)

	hits := decode[[]hitJSON](t, f.do(t, http.MethodGet, "/api/search?q=uniquebodyword&project=*", "", http.StatusOK))
	if len(hits) != 0 {
		t.Fatalf("search after delete = %+v, want none", hits)
	}
	f.do(t, http.MethodGet, "/api/item/"+id+"?project=*", "", http.StatusNotFound)
	f.do(t, http.MethodDelete, "/api/observation/"+id+"?project="+billing.Name, "", http.StatusNotFound)
}

func TestHealth(t *testing.T) {
	f := newFixture(t)
	empty := decode[healthJSON](t, f.do(t, http.MethodGet, "/api/health", "", http.StatusOK))
	if empty != (healthJSON{}) {
		t.Fatalf("empty health = %+v", empty)
	}
	for range 2 {
		if err := spool.Append(f.srv.Cfg.SpoolDir(), map[string]string{"event": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := f.srv.Store.RecordCaptureError(ctx, "PostToolUse", "old failure", time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.Store.RecordCaptureError(ctx, "Stop", "disk full", time.Now()); err != nil {
		t.Fatal(err)
	}
	got := decode[healthJSON](t, f.do(t, http.MethodGet, "/api/health", "", http.StatusOK))
	if want := (healthJSON{SpoolPending: 2, CaptureErrors24h: 1, LastError: "disk full"}); got != want {
		t.Fatalf("health = %+v, want %+v", got, want)
	}
}

func TestInternalErrorIsGeneric(t *testing.T) {
	f := newFixture(t)
	if err := f.srv.Store.Close(); err != nil {
		t.Fatal(err)
	}
	r := f.request(http.MethodGet, "/api/projects?token="+f.srv.Token, "")
	r.Header.Del("X-Mem-Token")
	w := f.serve(r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("closed store = %d, want 500", w.Code)
	}
	if got := decode[errorJSON](t, w); got.Error != "internal error" {
		t.Fatalf("500 body = %+v", got)
	}
	if !strings.Contains(f.log.String(), "list projects") {
		t.Fatalf("log = %q, want the full error", f.log.String())
	}
	if strings.Contains(f.log.String(), f.srv.Token) {
		t.Fatalf("log contains the token: %q", f.log.String())
	}
}

func TestUpdateOverCorruptFileIsInternalError(t *testing.T) {
	f := newFixture(t)
	id := f.save(t, billing, "corrupt me")
	m, err := f.srv.Store.GetMemory(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.srv.Cfg.WikiDir(), m.Path)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(b), "type: decision", "type: Not Valid", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	w := f.do(t, http.MethodPut, "/api/memory/"+id+"?project="+billing.Name, `{"title":"new title"}`, http.StatusInternalServerError)
	if got := decode[errorJSON](t, w); got.Error != "internal error" {
		t.Fatalf("500 body = %+v", got)
	}
}

func TestStarIsTheSameSnapshotEverywhere(t *testing.T) {
	f := newFixture(t)
	known := f.observe(t, "s1", billing.Key, "known", "one", testNow.Add(-2*time.Hour))
	user := f.observe(t, "s2", "user", "user wide", "two", testNow.Add(-1*time.Hour))
	orphan := f.observe(t, "s3", "/home/dev/src/unregistered", "orphan", "three", testNow)

	feed := decode[[]hitJSON](t, f.do(t, http.MethodGet, "/api/feed?project=*", "", http.StatusOK))
	if got, want := ids(feed), []string{known, user}; !reflect.DeepEqual(got, want) {
		t.Fatalf("feed * = %v, want %v", got, want)
	}
	for _, id := range ids(feed) {
		f.do(t, http.MethodGet, "/api/item/"+id+"?project=*", "", http.StatusOK)
	}
	f.do(t, http.MethodGet, "/api/item/"+orphan+"?project=*", "", http.StatusNotFound)
}

func TestIndexServedWithToken(t *testing.T) {
	f := newFixture(t)
	r := f.request(http.MethodGet, "/?token="+f.srv.Token, "")
	r.Header.Del("X-Mem-Token")
	w := f.serve(r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d %s, want 200", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(w.Body.String(), `id="feed"`) {
		t.Errorf("index has no feed: %q", w.Body.String())
	}
	if csp := w.Header().Get("Content-Security-Policy"); csp != "default-src 'self'; frame-ancestors 'none'" {
		t.Errorf("CSP = %q", csp)
	}
	if strings.Contains(w.Body.String(), f.srv.Token) {
		t.Error("index embeds the token")
	}
}

func TestIndexWithoutToken401(t *testing.T) {
	f := newFixture(t)
	r := f.request(http.MethodGet, "/", "")
	r.Header.Del("X-Mem-Token")
	if w := f.serve(r); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET / without token = %d, want 401", w.Code)
	}
	r = f.request(http.MethodGet, "/?token="+f.srv.Token, "")
	r.Host = "evil.example:47600"
	if w := f.serve(r); w.Code != http.StatusForbidden {
		t.Fatalf("GET / with bad Host = %d, want 403", w.Code)
	}
}

func TestAssetsEmbedded(t *testing.T) {
	f := newFixture(t)
	for path, want := range map[string]string{"/assets/app.js": "javascript", "/assets/app.css": "text/css"} {
		r := f.request(http.MethodGet, path, "")
		r.Header.Del("X-Mem-Token")
		w := f.serve(r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, want) {
			t.Errorf("GET %s Content-Type = %q, want %s", path, ct, want)
		}
		if w.Body.Len() == 0 {
			t.Errorf("GET %s is empty", path)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s without nosniff", path)
		}
		r.Host = "evil.example:47600"
		if w := f.serve(r); w.Code != http.StatusForbidden {
			t.Errorf("GET %s with bad Host = %d, want 403", path, w.Code)
		}
	}
	for _, path := range []string{"/assets/", "/assets/index.html", "/assets/nope.js", "/assets/../server.go"} {
		r := f.request(http.MethodGet, path, "")
		r.Header.Del("X-Mem-Token")
		if w := f.serve(r); w.Code == http.StatusOK {
			t.Errorf("GET %s = 200, want an error", path)
		}
	}
}

func TestAssetsRespectCSP(t *testing.T) {
	html, err := assetFS.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := assetFS.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	inline := regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>\s*[^<\s]|<style|\sstyle\s*=|\son[a-z]+\s*=|javascript:|https?://`)
	if m := inline.Find(html); m != nil {
		t.Errorf("index.html has inline code or an external URL: %q", m)
	}
	unsafe := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(|new Function|localStorage|sessionStorage|https?://`)
	if m := unsafe.Find(js); m != nil {
		t.Errorf("app.js uses %q", m)
	}
}
