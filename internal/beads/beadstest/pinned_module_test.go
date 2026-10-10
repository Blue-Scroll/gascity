package beadstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinnedBeadsModuleDirRefusesAnUnresolvedCache is the fence under the drift
// check's one silent-green path.
//
// The resolver is a hand-rolled copy of cmd/go's GOMODCACHE -> go env file ->
// GOPATH[0]/pkg/mod chain, kept inlined because shelling out to `go env` would
// grow the repo's shrink-only subprocess census. An inlined copy is only
// defensible if disagreeing with cmd/go is fatal, so that is asserted here
// rather than left to the one caller.
func TestPinnedBeadsModuleDirRefusesAnUnresolvedCache(t *testing.T) {
	t.Run("a cache that does not hold the module", func(t *testing.T) {
		if _, err := pinnedBeadsModuleDir(filepath.Join(t.TempDir(), "empty"), "v1.3.0"); err == nil {
			t.Fatal("pinnedBeadsModuleDir accepted a cache with no pinned module in it; a skip here lets a resolver bug read as green")
		}
	})

	t.Run("a path that is not a directory", func(t *testing.T) {
		cache := t.TempDir()
		path := filepath.Join(cache, filepath.FromSlash(PinnedBeadsModulePath)+"@v1.3.0")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not a module"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := pinnedBeadsModuleDir(cache, "v1.3.0"); err == nil {
			t.Fatal("pinnedBeadsModuleDir accepted a file where the module source should be")
		}
	})

	t.Run("the unpacked module", func(t *testing.T) {
		cache := t.TempDir()
		want := filepath.Join(cache, filepath.FromSlash(PinnedBeadsModulePath)+"@v1.3.0")
		if err := os.MkdirAll(want, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := pinnedBeadsModuleDir(cache, "v1.3.0")
		if err != nil {
			t.Fatalf("pinnedBeadsModuleDir: %v", err)
		}
		if got != want {
			t.Fatalf("pinnedBeadsModuleDir = %q, want %q", got, want)
		}
	})
}

// TestPinnedBeadsModuleDirFailsRatherThanSkips pins the seam itself.
//
// The subtests above prove the resolver returns an error; this proves what the
// caller does with it. A revert of the t.Fatalf to a t.Skipf would leave every
// other test in this package green while the pinned-cursor drift check silently
// stopped running — which is the failure mode it was written against, and it was
// reproduced with nothing more than GOMODCACHE pointed somewhere else.
func TestPinnedBeadsModuleDirFailsRatherThanSkips(t *testing.T) {
	reporter := &recordingModuleDirReporter{}
	if dir := pinnedBeadsModuleDirOrFatal(reporter, filepath.Join(t.TempDir(), "empty"), "v1.3.0"); dir != "" {
		t.Fatalf("an unresolved cache produced the directory %q", dir)
	}
	if len(reporter.skips) != 0 {
		t.Fatalf("an unresolved module cache was SKIPPED (%q); a skip lets the drift check go quiet and read as green", reporter.skips)
	}
	if len(reporter.fatals) != 1 {
		t.Fatalf("an unresolved module cache reported %d fatal(s), want exactly 1: %q", len(reporter.fatals), reporter.fatals)
	}
	for _, want := range []string{"GOMODCACHE", PinnedBeadsModulePath} {
		if !strings.Contains(reporter.fatals[0], want) {
			t.Errorf("the failure message does not name %q, so the operator cannot act on it:\n%s", want, reporter.fatals[0])
		}
	}
}

// recordingModuleDirReporter records what the resolver reports instead of
// failing or skipping the test that drives it. It does not call runtime.Goexit
// on Fatalf, so the caller returns normally and the zero value it hands back is
// asserted too.
type recordingModuleDirReporter struct {
	fatals []string
	skips  []string
}

func (r *recordingModuleDirReporter) Helper() {}

func (r *recordingModuleDirReporter) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *recordingModuleDirReporter) Skipf(format string, args ...any) {
	r.skips = append(r.skips, fmt.Sprintf(format, args...))
}

// TestBeadsGoModDirectivesTellRequireFromReplace pins the go.mod reader the
// drift check resolves its library through. The town links Blue-Scroll/beads
// `town` through a replace while go.mod still requires upstream v1.3.1, so
// reading the require where a replace exists compares gc's schema pins against
// a library no town binary links (vn-cuad16u).
func TestBeadsGoModDirectivesTellRequireFromReplace(t *testing.T) {
	cases := []struct {
		name          string
		gomod         string
		wantRequire   string
		wantTarget    string
		wantTargetVer string
		wantReplaced  bool
	}{
		{
			name:        "require block, no replace",
			gomod:       "module x\n\nrequire (\n\tgithub.com/other/mod v0.1.0\n\tgithub.com/steveyegge/beads v1.3.1\n)\n",
			wantRequire: "v1.3.1",
		},
		{
			name:        "one-line require with a comment",
			gomod:       "module x\nrequire github.com/steveyegge/beads v1.3.1 // indirect\n",
			wantRequire: "v1.3.1",
		},
		{
			// The shape `go mod edit -replace github.com/steveyegge/beads=../beads` writes.
			name:         "one-line replace with a local directory",
			gomod:        "module x\nrequire github.com/steveyegge/beads v1.3.1\nreplace github.com/steveyegge/beads => ../beads\n",
			wantRequire:  "v1.3.1",
			wantTarget:   "../beads",
			wantReplaced: true,
		},
		{
			// A replace block ABOVE the require: the old reader took the first
			// line naming beads and returned "=>" as the version.
			name:         "replace block above the require, with a left-hand version",
			gomod:        "module x\nreplace (\n\tgithub.com/steveyegge/beads v1.3.1 => /abs/beads\n)\nrequire github.com/steveyegge/beads v1.3.1\n",
			wantRequire:  "v1.3.1",
			wantTarget:   "/abs/beads",
			wantReplaced: true,
		},
		{
			name:          "replace with another module",
			gomod:         "module x\nrequire github.com/steveyegge/beads v1.3.1\nreplace github.com/steveyegge/beads => github.com/Blue-Scroll/beads v1.3.2\n",
			wantRequire:   "v1.3.1",
			wantTarget:    "github.com/Blue-Scroll/beads",
			wantTargetVer: "v1.3.2",
			wantReplaced:  true,
		},
		{
			name:        "a replace of some other module is not ours",
			gomod:       "module x\nrequire github.com/steveyegge/beads v1.3.1\nreplace github.com/other/mod => ../mod\n",
			wantRequire: "v1.3.1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinnedBeadsRequire(t, tc.gomod); got != tc.wantRequire {
				t.Errorf("require = %q, want %q", got, tc.wantRequire)
			}
			target, targetVer, ok := beadsReplacement(tc.gomod)
			if ok != tc.wantReplaced || target != tc.wantTarget || targetVer != tc.wantTargetVer {
				t.Errorf("replacement = (%q, %q, %v), want (%q, %q, %v)",
					target, targetVer, ok, tc.wantTarget, tc.wantTargetVer, tc.wantReplaced)
			}
		})
	}
}

// TestReplacedBeadsModuleDirResolvesOnlyALocalDirectory pins that a replace is
// resolved to the directory cmd/go would link, and that every shape it cannot
// resolve is an error, never a fall back to the required version.
func TestReplacedBeadsModuleDirResolvesOnlyALocalDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "gascity")
	beads := filepath.Join(parent, "beads")
	for _, dir := range []string{root, beads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a relative directory resolves from the module root", func(t *testing.T) {
		got, err := replacedBeadsModuleDir(root, "../beads", "")
		if err != nil {
			t.Fatalf("replacedBeadsModuleDir: %v", err)
		}
		if got != beads {
			t.Fatalf("replacedBeadsModuleDir = %q, want %q", got, beads)
		}
	})
	t.Run("an absolute directory is used as written", func(t *testing.T) {
		got, err := replacedBeadsModuleDir(root, beads, "")
		if err != nil || got != beads {
			t.Fatalf("replacedBeadsModuleDir = (%q, %v), want (%q, nil)", got, err, beads)
		}
	})
	t.Run("a missing directory is an error", func(t *testing.T) {
		if _, err := replacedBeadsModuleDir(root, "../no-such-beads", ""); err == nil {
			t.Fatal("a replace naming a directory that does not exist resolved")
		}
	})
	t.Run("a module replacement is refused, not guessed", func(t *testing.T) {
		if _, err := replacedBeadsModuleDir(root, "github.com/Blue-Scroll/beads", "v1.3.2"); err == nil {
			t.Fatal("a replace by another module resolved; the cache path for it is not computed here")
		}
	})
	t.Run("a bare word is neither a path nor a module", func(t *testing.T) {
		if _, err := replacedBeadsModuleDir(root, "beads", ""); err == nil {
			t.Fatal(`a replace target "beads" resolved; cmd/go reads it as neither a local path nor a module`)
		}
	})
}
