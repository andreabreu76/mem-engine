package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreabreu76/mem-engine/internal/redact"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func TestSaveAndUpdateRedactSecrets(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	r, err := redact.New([]string{`acme-pin-[0-9]{4}`}, false)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Redactor = r
	token := "ghp_" + strings.Repeat("a", 36)
	pin := "acme-pin-" + "4821"
	in := Input{
		Type:    "fact",
		Title:   "Deploy token " + token,
		Summary: "use " + token + " for deploys",
		Body:    "token " + token + "\n<private>vault notes</private>pin " + pin + "\n",
		Tags:    []string{"deploy", token},
	}
	m, err := f.svc.Save(ctx, billing, in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if m.Title != "Deploy token [REDACTED:github]" || !strings.Contains(m.Body, "[REDACTED:extra]") || m.Tags[1] != "[REDACTED:github]" {
		t.Errorf("saved = %+v", m)
	}
	if strings.Contains(m.Path, strings.Repeat("a", 20)) {
		t.Errorf("path = %q", m.Path)
	}
	summary := "rotated " + token
	body := "new body " + pin + " " + token
	tags := []string{token}
	if _, err := f.svc.Update(ctx, m.ID, Patch{Summary: &summary, Body: &body, Tags: &tags}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	title := "  "
	if _, err := f.svc.Update(ctx, m.ID, Patch{Title: &title}); err == nil {
		t.Errorf("Update with blank title succeeded")
	}
	private := "<private>" + token + "</private>"
	if _, err := f.svc.Update(ctx, m.ID, Patch{Title: &private}); err == nil {
		t.Errorf("Update with private-only title succeeded")
	}

	secrets := []string{token, pin, "vault notes"}
	file, err := os.ReadFile(filepath.Join(f.svc.WikiRoot, filepath.FromSlash(m.Path)))
	if err != nil {
		t.Fatal(err)
	}
	history := gitOut(t, filepath.Join(f.svc.WikiRoot, billing.Dir), "log", "-p", "--all")
	got, err := f.svc.Store.GetMemory(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	indexed := strings.Join(append([]string{got.Title, got.Summary, got.Body}, got.Tags...), "\n")
	for _, secret := range secrets {
		for where, text := range map[string]string{"file": string(file), "git history": history, "index": indexed} {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains %q:\n%s", where, secret, text)
			}
		}
	}
	hits, err := f.svc.Store.Search(ctx, store.SearchParams{Query: strings.Repeat("a", 36), Limit: 10, Scope: store.CurrentScope(billing.Key)})
	if err != nil || len(hits) != 0 {
		t.Errorf("Search for token = %+v, %v", hits, err)
	}
}
