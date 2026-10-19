package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

// countingFS counts ReadFile calls per path, so a test can prove a fragment
// was parsed once per build and not once per agent. It also serializes the
// Fake's reads, which append to its unguarded spy log.
type countingFS struct {
	*fsys.Fake
	mu    sync.Mutex
	reads map[string]int
}

func (c *countingFS) ReadFile(name string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads[name]++
	return c.Fake.ReadFile(name)
}

func (c *countingFS) ReadDir(name string) ([]os.DirEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Fake.ReadDir(name)
}

// fragmentCacheFixture is a city with one imported pack, city-root fragments
// and two agents whose prompts each have their own template-fragments/. It
// covers every layer promptFragmentDirs lists, plus templateFirst, the
// session func and inject fragments.
func fragmentCacheFixture() *countingFS {
	f := fsys.NewFake()
	f.Files["/pack/prompts/shared/base.template.md"] = []byte(
		`{{ define "greet" }}pack greet {{ .AgentName }}{{ end }}` +
			`{{ define "note-default" }}pack note{{ end }}` +
			`{{ define "tail" }}pack tail for {{ .RigName }}{{ end }}`)
	f.Files["/pack/template-fragments/rules.template.md"] = []byte(
		`{{ define "rules" }}pack rules, session {{ session "mayor" }}{{ end }}`)
	f.Files["/city/template-fragments/city.template.md"] = []byte(
		`{{ define "greet" }}city greet {{ .AgentName }}{{ end }}`)
	f.Files["/city/agents/a/prompt.template.md"] = []byte(
		`{{ template "greet" . }} | {{ template "rules" . }} | {{ templateFirst . "note-a" "note-default" }}`)
	f.Files["/city/agents/a/template-fragments/own.template.md"] = []byte(
		`{{ define "note-a" }}a's own note{{ end }}`)
	f.Files["/city/agents/b/prompt.template.md"] = []byte(
		`{{ define "rules" }}b overrides rules{{ end }}` +
			`{{ template "greet" . }} | {{ template "rules" . }} | {{ templateFirst . "note-a" "note-default" }}`)
	return &countingFS{Fake: f, reads: make(map[string]int)}
}

func renderFixtureAgent(cache *promptFragmentCache, fs fsys.FS, agent string, stderr io.Writer) PromptRenderResult {
	return renderPromptWithFragments(cache, fs, "/city", "town", "agents/"+agent+"/prompt.template.md",
		PromptContext{AgentName: agent, RigName: "rig-" + agent}, "", stderr, []string{"/pack"}, []string{"tail"}, nil)
}

// The cache must change nothing about what a prompt renders, whatever order
// agents render in: same text, same SHA, as the uncached path.
func TestPromptFragmentCacheRendersLikeUncached(t *testing.T) {
	fs := fragmentCacheFixture()
	want := map[string]PromptRenderResult{
		"a": renderFixtureAgent(nil, fs, "a", io.Discard),
		"b": renderFixtureAgent(nil, fs, "b", io.Discard),
	}
	if got := want["a"].Text; got != "city greet a | pack rules, session "+lookupSessionNameOrLegacy(nil, "town", "mayor", "")+" | a's own note\n\npack tail for rig-a" {
		t.Fatalf("uncached render of a = %q; the fixture no longer covers every layer", got)
	}
	for _, order := range [][]string{{"a", "b", "a", "b"}, {"b", "a", "b", "a"}} {
		cache := newPromptFragmentCache()
		for _, agent := range order {
			if got := renderFixtureAgent(cache, fs, agent, io.Discard); got != want[agent] {
				t.Errorf("order %v: cached render of %s = %+v, want %+v", order, agent, got, want[agent])
			}
		}
	}
}

// One render's body must never leak into the cached set: b defines its own
// "rules" and a must still get the pack's, and a's own note must not reach b.
func TestPromptFragmentCacheKeepsEachBodyToItsRender(t *testing.T) {
	fs := fragmentCacheFixture()
	cache := newPromptFragmentCache()
	b := renderFixtureAgent(cache, fs, "b", io.Discard).Text
	a := renderFixtureAgent(cache, fs, "a", io.Discard).Text
	if !strings.Contains(b, "b overrides rules") || strings.Contains(b, "a's own note") {
		t.Errorf("b = %q, want its own rules and the pack note", b)
	}
	if !strings.Contains(a, "pack rules") || strings.Contains(a, "b overrides") {
		t.Errorf("a = %q, want the pack's rules, not b's", a)
	}
}

// The point of the cache: a shared fragment is read once per build, however
// many agents render.
func TestPromptFragmentCacheReadsEachFragmentOncePerBuild(t *testing.T) {
	fs := fragmentCacheFixture()
	cache := newPromptFragmentCache()
	for range 5 {
		renderFixtureAgent(cache, fs, "a", io.Discard)
		renderFixtureAgent(cache, fs, "b", io.Discard)
	}
	for _, path := range []string{
		"/pack/prompts/shared/base.template.md",
		"/pack/template-fragments/rules.template.md",
		"/city/template-fragments/city.template.md",
		"/city/agents/a/template-fragments/own.template.md",
	} {
		if got := fs.reads[path]; got != 1 {
			t.Errorf("%s read %d times over 10 renders, want 1", path, got)
		}
	}
	// The prompt body itself is per render, not a fragment.
	if got := fs.reads["/city/agents/a/prompt.template.md"]; got != 5 {
		t.Errorf("a's prompt read %d times, want 5 (one per render)", got)
	}
}

// A nil cache is the old path: every render reads every fragment again, so a
// one-off render (gc prime) sees an edit at once.
func TestPromptFragmentCacheNilParsesEveryRender(t *testing.T) {
	fs := fragmentCacheFixture()
	renderFixtureAgent(nil, fs, "a", io.Discard)
	fs.Files["/pack/prompts/shared/base.template.md"] = []byte(`{{ define "note-default" }}x{{ end }}{{ define "tail" }}edited tail{{ end }}`)
	if got := renderFixtureAgent(nil, fs, "a", io.Discard).Text; !strings.HasSuffix(got, "edited tail") {
		t.Errorf("nil-cache render after an edit = %q, want the edited tail", got)
	}
}

// A broken fragment is reported once per build, and the render still uses
// every fragment that parsed, exactly as the uncached path does.
func TestPromptFragmentCacheBrokenFragmentWarnsOncePerBuild(t *testing.T) {
	fs := fragmentCacheFixture()
	fs.Files["/pack/template-fragments/broken.template.md"] = []byte(`{{ define "x" }}{{ if }}{{ end }}`)
	var uncachedErr bytes.Buffer
	want := renderFixtureAgent(nil, fs, "a", &uncachedErr)
	if !strings.Contains(uncachedErr.String(), "broken.template.md") {
		t.Fatalf("uncached stderr = %q, want the broken fragment named", uncachedErr.String())
	}
	cache := newPromptFragmentCache()
	var cachedErr bytes.Buffer
	for range 3 {
		if got := renderFixtureAgent(cache, fs, "a", &cachedErr); got != want {
			t.Errorf("cached render = %+v, want %+v", got, want)
		}
	}
	if n := strings.Count(cachedErr.String(), "broken.template.md"); n != 1 {
		t.Errorf("broken fragment reported %d times over 3 renders, want 1: %q", n, cachedErr.String())
	}
}

// resolveTemplate renders concurrently in some callers; the cache must hold.
func TestPromptFragmentCacheConcurrentRenders(t *testing.T) {
	fs := fragmentCacheFixture()
	want := renderFixtureAgent(nil, fs, "a", io.Discard)
	cache := newPromptFragmentCache()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := renderFixtureAgent(cache, fs, "a", io.Discard); got != want {
				t.Errorf("concurrent render = %+v, want %+v", got, want)
			}
		}()
	}
	wg.Wait()
}
