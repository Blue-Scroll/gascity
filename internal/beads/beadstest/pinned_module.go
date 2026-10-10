package beadstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PinnedBeadsModulePath is the module gc links its native store against.
const PinnedBeadsModulePath = "github.com/steveyegge/beads"

// PinnedBeadsModuleDir resolves the unpacked source directory of the beads
// version this module requires, and FAILS the calling test when it cannot.
//
// Failing rather than skipping is the point. The packages that call this import
// github.com/steveyegge/beads, so if the test binary compiled at all then cmd/go
// resolved the pinned module — which means an absent directory does not say "the
// module is not here", it says the resolution below disagreed with cmd/go's. That
// is exactly the situation the drift check must shout about: a skip would let a
// resolver bug read as green, and the check it silences is the one standing
// between gc and a library that migrates somebody's shared database on open. A
// quiet skip was demonstrated with nothing more than GOMODCACHE pointed
// somewhere else.
//
// It resolves the cache the way the go command does rather than by reading
// GOMODCACHE, because `go test` does not export that variable into the test
// process: a check that read it ran only under wrappers that happen to forward
// it, and went quiet under every plain `go test`, an editor runner and a git
// hook. A contract check that is honest under one wrapper is not a contract
// check. (internal/beads/contract/migrate_journal_test.go learned this the hard
// way and states the reasoning at length.)
//
// It also deliberately shells out to nothing. `go env` or `go list -m` would
// answer in one line, and a subprocess in a test grows the repo's source
// resource census — a shrink-only ratchet — so the resolution is inlined
// instead.
//
// A go.mod `replace` of beads with a local directory wins over the required
// version, exactly as it does for cmd/go, because the replacement is the library
// that gets linked. The town builds gc this way: Blue-Scroll/gascity `town` is
// built with `go mod edit -replace github.com/steveyegge/beads=../beads` against
// Blue-Scroll/beads `town`, which carries migrations upstream does not (main
// 0067, ignored 0027, vn-s54d6fy). Reading the required v1.3.1 there compared
// the pins against a library no town binary links, so the pins went stale and
// the native store was refused on every ledger (vn-cuad16u).
func PinnedBeadsModuleDir(t *testing.T) string {
	t.Helper()
	// Under bazel the module tree arrives as runfiles from the go_deps
	// external repository (the test declares the schema library as data);
	// a pure-Bazel machine has no go module cache at all.
	if dir := bazelRunfilesBeadsModule(); dir != "" {
		return dir
	}
	root := RepositoryRoot(t)
	gomod := readGoMod(t, root)
	if target, targetVersion, ok := beadsReplacement(gomod); ok {
		dir, err := replacedBeadsModuleDir(root, target, targetVersion)
		if err != nil {
			t.Fatalf("%v\n"+
				"go.mod replaces %s, so the replacement is the library this build links. The drift check "+
				"must read that one, and must not fall back to the required version, which is not linked.",
				err, PinnedBeadsModulePath)
		}
		return dir
	}
	return pinnedBeadsModuleDirOrFatal(t, goModuleCache(t), pinnedBeadsRequire(t, gomod))
}

// PinnedBeadsSource names the beads library PinnedBeadsModuleDir reads, for a
// failure message: the required version, plus the replacement when go.mod has
// one. A message that printed only "v1.3.1" under a replace would send the
// reader to the wrong library.
func PinnedBeadsSource(t *testing.T) string {
	t.Helper()
	gomod := readGoMod(t, RepositoryRoot(t))
	version := pinnedBeadsRequire(t, gomod)
	if target, targetVersion, ok := beadsReplacement(gomod); ok {
		return strings.TrimSpace(version + " replaced by " + target + " " + targetVersion)
	}
	return version
}

// replacedBeadsModuleDir resolves a beads replacement to a directory. Only a
// local directory is resolved. A replacement by another module path is refused
// rather than guessed at: the cache path for it needs cmd/go's case escaping,
// and a wrong guess here is a drift check reading the wrong library.
func replacedBeadsModuleDir(root, target, targetVersion string) (string, error) {
	if targetVersion != "" {
		return "", fmt.Errorf("go.mod replaces %s with the module %s %s; only a local directory replacement is resolved here",
			PinnedBeadsModulePath, target, targetVersion)
	}
	// cmd/go's own rule: a replacement is a local path when it is absolute or
	// starts with ./ or ../ (filepath.IsAbs covers a Windows drive letter too).
	if !filepath.IsAbs(target) && !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") {
		return "", fmt.Errorf("go.mod replaces %s with %q, which is neither a local path nor a module path with a version",
			PinnedBeadsModulePath, target)
	}
	dir := target
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, filepath.FromSlash(target))
	}
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return "", fmt.Errorf("go.mod replaces %s with %s, which cannot be read: %w", PinnedBeadsModulePath, dir, err)
	case !info.IsDir():
		return "", fmt.Errorf("go.mod replaces %s with %s, which is not a directory", PinnedBeadsModulePath, dir)
	default:
		return dir, nil
	}
}

// bazelRunfilesBeadsModule locates the pinned beads module inside the bazel
// runfiles tree, matching any repository directory whose name ends in the
// go_deps canonical suffix and carrying the module's migration directories.
func bazelRunfilesBeadsModule() string {
	for _, rf := range []string{os.Getenv("RUNFILES_DIR"), os.Getenv("TEST_SRCDIR")} {
		if rf == "" {
			continue
		}
		entries, err := os.ReadDir(rf)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.Contains(name, "go_deps+com_github_steveyegge_beads") {
				continue
			}
			dir := filepath.Join(rf, name)
			if info, err := os.Stat(filepath.Join(dir, "internal", "storage", "schema", "migrations")); err == nil && info.IsDir() {
				return dir
			}
		}
	}
	return ""
}

// moduleDirReporter is the subset of *testing.T the seam below uses.
//
// It exists so that "an unresolved cache is FATAL, never a skip" can be asserted
// by a recorder in this package's own tests. The only other way to observe it is
// a *testing.T in a subprocess, and a subprocess in a test grows the repo's
// shrink-only resource census — the same reason this file resolves the module
// cache inline instead of asking `go env`. Skipf is part of the interface on
// purpose: a revert to it still compiles, and is caught by a test rather than by
// a reviewer.
type moduleDirReporter interface {
	Helper()
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
}

// pinnedBeadsModuleDirOrFatal reports an unresolved module cache as a test
// failure. See PinnedBeadsModuleDir for why it cannot be a skip.
func pinnedBeadsModuleDirOrFatal(t moduleDirReporter, cache, version string) string {
	t.Helper()
	dir, err := pinnedBeadsModuleDir(cache, version)
	if err != nil {
		t.Fatalf("%v\n"+
			"The test binary links %s, so the go command resolved it; this resolution did not. "+
			"Check GOMODCACHE, GOPATH and GOENV (resolved cache: %s), and whether the build moved "+
			"to vendor mode — the pinned-cursor drift check cannot run "+
			"without the module's own migration directories, and it must not pass without running.",
			err, PinnedBeadsModulePath, cache)
	}
	return dir
}

// pinnedBeadsModuleDir is the resolution itself, separated from the test so that
// the failure path has a test of its own.
func pinnedBeadsModuleDir(cache, version string) (string, error) {
	dir := filepath.Join(cache, filepath.FromSlash(PinnedBeadsModulePath)+"@"+version)
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return "", fmt.Errorf("pinned beads %s is not unpacked in the module cache at %s: %w", version, dir, err)
	case !info.IsDir():
		return "", fmt.Errorf("pinned beads %s resolved to %s, which is not a directory", version, dir)
	default:
		return dir, nil
	}
}

// PinnedBeadsVersion reads the beads version this module requires from go.mod.
// Under a replace it is NOT the linked library; PinnedBeadsSource says which is.
func PinnedBeadsVersion(t *testing.T) string {
	t.Helper()
	return pinnedBeadsRequire(t, readGoMod(t, RepositoryRoot(t)))
}

func readGoMod(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	return string(data)
}

func pinnedBeadsRequire(t *testing.T, gomod string) string {
	t.Helper()
	for _, d := range beadsGoModDirectives(gomod) {
		if d.verb == "require" && len(d.args) >= 1 {
			return d.args[0]
		}
	}
	t.Fatalf("go.mod does not require %s", PinnedBeadsModulePath)
	return ""
}

// beadsReplacement returns the right-hand side of go.mod's replace for beads:
// the target, plus its version when the target is a module rather than a
// directory. ok is false when go.mod replaces nothing.
func beadsReplacement(gomod string) (target, targetVersion string, ok bool) {
	for _, d := range beadsGoModDirectives(gomod) {
		if d.verb != "replace" {
			continue
		}
		for i, arg := range d.args {
			if arg != "=>" || i+1 >= len(d.args) {
				continue
			}
			target = d.args[i+1]
			if i+2 < len(d.args) {
				targetVersion = d.args[i+2]
			}
			return target, targetVersion, true
		}
	}
	return "", "", false
}

// beadsGoModDirective is one go.mod line about the beads module: the verb it
// sits under, inline or in a block, and the fields after the module path.
type beadsGoModDirective struct {
	verb string
	args []string
}

// beadsGoModDirectives reads every require and replace of beads out of go.mod,
// in both the one-line and the block form. Telling the verbs apart is the
// point: the old reader took the first line naming beads, so a replace block
// above the require would have handed back "=>" as the version.
func beadsGoModDirectives(gomod string) []beadsGoModDirective {
	var out []beadsGoModDirective
	block := ""
	for _, line := range strings.Split(gomod, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
		case block != "" && fields[0] == ")":
			block = ""
		case block != "":
			if fields[0] == PinnedBeadsModulePath {
				out = append(out, beadsGoModDirective{verb: block, args: fields[1:]})
			}
		case len(fields) == 2 && fields[1] == "(":
			block = fields[0]
		case len(fields) >= 2 && fields[1] == PinnedBeadsModulePath:
			out = append(out, beadsGoModDirective{verb: fields[0], args: fields[2:]})
		}
	}
	return out
}

// RepositoryRoot walks up from the working directory to the module root.
func RepositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory")
		}
		dir = parent
	}
}

// goModuleCache resolves the module cache the way cmd/go/internal/cfg does: the
// process environment first, then the `go env -w` config file ($GOENV, else
// os.UserConfigDir()/go/env, with "off" meaning no file), then the default
// $GOPATH/pkg/mod with GOPATH resolved by the same two steps and defaulting to
// $HOME/go. Only the first element of a GOPATH list holds the module cache.
func goModuleCache(t *testing.T) string {
	t.Helper()
	if dir := goEnvValue("GOMODCACHE"); dir != "" {
		return dir
	}
	gopath := goEnvValue("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("resolve home directory to default GOPATH: %v", err)
		}
		gopath = filepath.Join(home, "go")
	}
	roots := filepath.SplitList(gopath)
	if len(roots) == 0 || roots[0] == "" {
		t.Fatalf("GOPATH %q has no usable first element", gopath)
	}
	return filepath.Join(roots[0], "pkg", "mod")
}

// goEnvValue reads one go environment variable: process environment, then the
// `go env -w` config file.
func goEnvValue(name string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return goEnvFileValue(name)
}

// goEnvFileValue reads one key out of the go env config file.
func goEnvFileValue(name string) string {
	path := strings.TrimSpace(os.Getenv("GOENV"))
	switch path {
	case "off":
		return ""
	case "", "auto":
		dir, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(dir, "go", "env")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- the go env config path, resolved the way the go command resolves it
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
