package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/docgen"
)

func TestGenDocProducesMarkdown(t *testing.T) {
	var buf bytes.Buffer
	root := newRootCmd(&buf, &buf)

	// Render to buffer using the renderer directly (avoids needing repo root
	// for the go.mod check in the RunE handler).
	var md bytes.Buffer
	if err := docgen.RenderCLIMarkdown(&md, root); err != nil {
		t.Fatalf("RenderCLIMarkdown: %v", err)
	}

	out := md.String()
	if out == "" {
		t.Fatal("empty markdown output")
	}

	// Check known visible commands exist.
	for _, cmd := range []string{"gc init", "gc start", "gc stop", "gc agent", "gc rig add", "gc mail"} {
		if !strings.Contains(out, "## "+cmd) {
			t.Errorf("missing command %q in CLI reference", cmd)
		}
	}

	// Check hidden commands are absent.
	if strings.Contains(out, "## gc gen-doc") {
		t.Error("hidden command gen-doc should not appear")
	}

	// Check basic structure: frontmatter title, never a body H1 (Mintlify
	// renders the title; a body H1 would duplicate it).
	if !strings.Contains(out, `title: "CLI Reference"`) {
		t.Error("missing CLI Reference frontmatter title")
	}
	if strings.Contains(out, "# CLI Reference") {
		t.Error("body H1 duplicates the frontmatter title")
	}
	if !strings.Contains(out, "Auto-generated") {
		t.Error("missing auto-generated note")
	}
}

func TestGenDocImportAddDocumentsSourceLanes(t *testing.T) {
	var buf bytes.Buffer
	root := newRootCmd(&buf, &buf)

	var md bytes.Buffer
	if err := docgen.RenderCLIMarkdown(&md, root); err != nil {
		t.Fatalf("RenderCLIMarkdown: %v", err)
	}

	section, ok := cliDocSection(md.String(), "gc import add")
	if !ok {
		t.Fatal("missing gc import add section")
	}
	for _, want := range []string{
		"local paths outside git worktrees",
		"remote git repositories",
		"remote GitHub repository subpaths",
		"Registry catalog handles are lookup shortcuts",
		"source and optional version",
		"local binding name",
		"display/advisory metadata",
	} {
		if !strings.Contains(section, want) {
			t.Fatalf("gc import add docs missing %q:\n%s", want, section)
		}
	}
}

func TestGenDocImportAddExamplesAvoidRejectedSourceRefs(t *testing.T) {
	var buf bytes.Buffer
	root := newRootCmd(&buf, &buf)

	var md bytes.Buffer
	if err := docgen.RenderCLIMarkdown(&md, root); err != nil {
		t.Fatalf("RenderCLIMarkdown: %v", err)
	}

	section, ok := cliDocSection(md.String(), "gc import add")
	if !ok {
		t.Fatal("missing gc import add section")
	}
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "gc import add ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			t.Fatalf("gc import add example missing source: %q", line)
		}
		source := fields[3]
		if isRemoteImportSource(source) && hasRepositoryRefInSource(source) {
			t.Fatalf("gc import add example uses rejected source ref %q in:\n%s", source, section)
		}
	}
}

// TestCLIDocsFreshness byte-compares docs/reference/cli.md against a freshly
// rendered copy. The file says "Auto-generated, do not edit", and this test is
// what makes that true: ANY drift goes red, whether it is a new command, a
// changed Long string, a new flag, or a hand edit.
//
// It used to check only that every command had a section, plus a hand-written
// list of seven sections. So a changed help string on any of the other 240
// commands was invisible, and docs/reference/cli.md drifted anyway (vn-dnv2cm2).
//
// It runs the real `gc gen-doc` through Execute rather than calling the
// renderer. Execute changes the tree before gen-doc walks it: it adds the
// `completion` and `help` commands, and it merges the root's persistent flags,
// which is what turns the root's synopsis into `gc [flags]`. A render that skips
// Execute misses all three and can never match the file (measured on
// vn-s9oydtn). Running the command itself means this test cannot drift from
// the generator.
//
// rootCommandOptions{} leaves pack discovery off, so the tree is the same on
// every machine. A doc regenerated from inside a city does go red here, which
// is correct: such a page carries that city's pack commands, and no other
// machine has them.
func TestCLIDocsFreshness(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	committedPath := filepath.Join(repoRoot, "docs", "reference", "cli.md")
	committed, err := os.ReadFile(committedPath)
	if err != nil {
		t.Fatalf("reading %s: %v\nRun: go run ./cmd/gc gen-doc", committedPath, err)
	}

	// gen-doc refuses to run without a go.mod in the working directory, and it
	// writes docs/reference/cli.md under it. A scratch directory with an empty
	// go.mod keeps the real file untouched.
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(scratch)

	var stderr bytes.Buffer
	root := newRootCmdWithOptions(io.Discard, &stderr, rootCommandOptions{})
	root.SetArgs([]string{"gen-doc"})
	if err := root.Execute(); err != nil {
		t.Fatalf("gc gen-doc: %v\n%s", err, stderr.String())
	}
	generated, err := os.ReadFile(filepath.Join(scratch, "docs", "reference", "cli.md"))
	if err != nil {
		t.Fatalf("reading the generated doc: %v", err)
	}

	want := string(generated)
	got := string(committed)
	if got == want {
		return
	}

	line, section, gotLine, wantLine := firstCLIDocDifference(got, want)
	t.Errorf("docs/reference/cli.md is stale. Run: go run ./cmd/gc gen-doc\n"+
		"First difference at line %d, in section %q:\n  committed: %q\n  generated: %q",
		line, section, gotLine, wantLine)
}

// firstCLIDocDifference finds the first line where the committed file and the
// freshly rendered one disagree. It returns the 1-based line number, the
// section that line sits in, and both lines. A line past the end of one side
// reads as an empty string, so a file that is only shorter still names the
// place it stops.
func firstCLIDocDifference(got, want string) (line int, section, gotLine, wantLine string) {
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")

	at := func(lines []string, i int) string {
		if i < len(lines) {
			return lines[i]
		}
		return ""
	}

	longest := len(gotLines)
	if len(wantLines) > longest {
		longest = len(wantLines)
	}
	for i := range longest {
		g, w := at(gotLines, i), at(wantLines, i)
		if g == w {
			continue
		}
		// Name the section from whichever side has a heading here. A section the
		// committed file is missing shows up as a heading on the generated side.
		names := gotLines
		if strings.HasPrefix(w, "## ") && !strings.HasPrefix(g, "## ") {
			names = wantLines
		}
		return i + 1, cliDocSectionAt(names, i), g, w
	}
	return 0, "", "", ""
}

// cliDocSectionAt names the nearest "## " heading at or above line i. A
// difference in the frontmatter, before any heading, belongs to no section.
func cliDocSectionAt(lines []string, i int) string {
	if i >= len(lines) {
		i = len(lines) - 1
	}
	for ; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "## ") {
			return strings.TrimPrefix(lines[i], "## ")
		}
	}
	return "(before the first section)"
}

func cliDocSection(doc, command string) (string, bool) {
	heading := "## " + command + "\n"
	start := strings.Index(doc, heading)
	if start < 0 {
		return "", false
	}
	rest := doc[start+len(heading):]
	next := strings.Index(rest, "\n## ")
	if next < 0 {
		return doc[start:], true
	}
	return doc[start : start+len(heading)+next+1], true
}
