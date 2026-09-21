package workdir

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func intPtr(n int) *int { return &n }

func TestResolveWorkDirPathUsesWorkDirTemplate(t *testing.T) {
	cityPath := t.TempDir()
	cityName := "gastown"
	cfg := &config.City{
		Workspace: config.Workspace{Name: cityName},
		Rigs:      []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}},
	}
	agent := config.Agent{
		Name:    "refinery",
		Dir:     "demo",
		WorkDir: ".gc/worktrees/{{.Rig}}/{{.AgentBase}}",
	}

	got := ResolveWorkDirPath(cityPath, cityName, "demo/refinery", agent, cfg.Rigs)
	want := filepath.Join(cityPath, ".gc", "worktrees", "demo", "refinery")
	if got != want {
		t.Fatalf("ResolveWorkDirPath() = %q, want %q", got, want)
	}
}

func TestResolveWorkDirPathUsesWorktreesRootTemplate(t *testing.T) {
	cityPath := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	t.Setenv("GC_WORKTREES_DIR", worktreesRoot)
	t.Setenv("T3CODE_WORKTREES_DIR", filepath.Join(t.TempDir(), "ignored"))
	t.Setenv("T3CODE_HOME", filepath.Join(t.TempDir(), "ignored-home"))

	agent := config.Agent{
		Name:    "worker",
		Dir:     "demo",
		WorkDir: "{{.WorktreesRoot}}/gascity/{{.CityName}}/{{.Rig}}/jj/agents/{{.AgentBase}}",
	}
	rigs := []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}}

	got := ResolveWorkDirPath(cityPath, "gastown", "demo/worker-1", agent, rigs)
	want := filepath.Join(worktreesRoot, "gascity", "gastown", "demo", "jj", "agents", "worker-1")
	if got != want {
		t.Fatalf("ResolveWorkDirPath() = %q, want %q", got, want)
	}
}

func TestResolveWorkDirPathDefaultsRigScopedAgentsToRigRoot(t *testing.T) {
	cityPath := t.TempDir()
	rigRoot := filepath.Join(t.TempDir(), "demo-repo")
	got := ResolveWorkDirPath(cityPath, "gastown", "demo/refinery", config.Agent{
		Name: "refinery",
		Dir:  "demo",
	}, []config.Rig{{Name: "demo", Path: rigRoot}})
	if got != rigRoot {
		t.Fatalf("ResolveWorkDirPath() = %q, want %q", got, rigRoot)
	}
}

func TestResolveWorkDirPathUsesPoolInstanceBase(t *testing.T) {
	cityPath := t.TempDir()
	got := ResolveWorkDirPath(cityPath, "gastown", "demo/polecat-2", config.Agent{
		Name:              "polecat",
		Dir:               "demo",
		WorkDir:           ".gc/worktrees/{{.Rig}}/polecats/{{.AgentBase}}",
		MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3),
	}, []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}})
	want := filepath.Join(cityPath, ".gc", "worktrees", "demo", "polecats", "polecat-2")
	if got != want {
		t.Fatalf("ResolveWorkDirPath() = %q, want %q", got, want)
	}
}

// TestResolveWorkDirPathGivesEachPoolSlotUniqueWorktree is the #774 regression
// guard: N pool workers sharing one template must each resolve to a distinct
// worktree path derived from their namepool slot, not the template base.
func TestResolveWorkDirPathGivesEachPoolSlotUniqueWorktree(t *testing.T) {
	cityPath := t.TempDir()
	rigs := []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}}
	agent := config.Agent{
		Name:              "ant",
		Dir:               "demo",
		WorkDir:           ".gc/worktrees/{{.Rig}}/ants/{{.AgentBase}}",
		MinActiveSessions: intPtr(0),
		MaxActiveSessions: intPtr(4),
	}

	cases := []struct {
		slot string
		want string
	}{
		{slot: "demo/ant-fenrir", want: filepath.Join(cityPath, ".gc", "worktrees", "demo", "ants", "ant-fenrir")},
		{slot: "demo/ant-grendel", want: filepath.Join(cityPath, ".gc", "worktrees", "demo", "ants", "ant-grendel")},
		{slot: "demo/ant-hati", want: filepath.Join(cityPath, ".gc", "worktrees", "demo", "ants", "ant-hati")},
		{slot: "demo/ant-skoll", want: filepath.Join(cityPath, ".gc", "worktrees", "demo", "ants", "ant-skoll")},
	}

	seen := make(map[string]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.slot, func(t *testing.T) {
			got := ResolveWorkDirPath(cityPath, "gastown", tc.slot, agent, rigs)
			if got != tc.want {
				t.Fatalf("ResolveWorkDirPath(%q) = %q, want %q", tc.slot, got, tc.want)
			}
			if prev, dup := seen[got]; dup {
				t.Fatalf("slot %q collided with %q on path %q", tc.slot, prev, got)
			}
			seen[got] = tc.slot
		})
	}

	if len(seen) != len(cases) {
		t.Fatalf("unique paths = %d, want %d", len(seen), len(cases))
	}
}

func TestSessionQualifiedNameCanonicalizesBareAndQualifiedPoolAliases(t *testing.T) {
	cityPath := t.TempDir()
	rigs := []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}}
	agent := config.Agent{
		Name:              "polecat",
		Dir:               "demo",
		MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3),
	}

	bare := SessionQualifiedName(cityPath, agent, rigs, "polecat-fenrir", "")
	qualified := SessionQualifiedName(cityPath, agent, rigs, "demo/polecat-fenrir", "")
	if bare != "demo/polecat-fenrir" {
		t.Fatalf("SessionQualifiedName(bare) = %q, want %q", bare, "demo/polecat-fenrir")
	}
	if qualified != bare {
		t.Fatalf("SessionQualifiedName(qualified) = %q, want %q", qualified, bare)
	}
}

func TestSessionQualifiedNameKeepsSingletonTemplateIdentity(t *testing.T) {
	cityPath := t.TempDir()
	rigs := []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}}
	agent := config.Agent{Name: "witness", Dir: "demo", MaxActiveSessions: intPtr(1)}

	if got := SessionQualifiedName(cityPath, agent, rigs, "demo/boot", ""); got != "demo/witness" {
		t.Fatalf("SessionQualifiedName() = %q, want %q", got, "demo/witness")
	}
}

func TestSessionQualifiedNameUsesSingletonExplicitNameWhenAliasEmpty(t *testing.T) {
	cityPath := t.TempDir()
	rigs := []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}}
	agent := config.Agent{Name: "witness", Dir: "demo", MaxActiveSessions: intPtr(1)}

	if got := SessionQualifiedName(cityPath, agent, rigs, "", "crew--gastown"); got != "demo/crew--gastown" {
		t.Fatalf("SessionQualifiedName() = %q, want singleton tmux_alias explicit name in work_dir identity", got)
	}
}

func TestSessionQualifiedNamePreservesRigQualifiedBindingIdentity(t *testing.T) {
	cityPath := t.TempDir()
	rigs := []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}}
	agent := config.Agent{
		Name:              "worker",
		Dir:               "demo",
		BindingName:       "ops",
		MinActiveSessions: intPtr(0),
		MaxActiveSessions: intPtr(2),
	}

	if got := SessionQualifiedName(cityPath, agent, rigs, "ops.worker-1", ""); got != "demo/ops.worker-1" {
		t.Fatalf("SessionQualifiedName(bare binding) = %q, want %q", got, "demo/ops.worker-1")
	}
	if got := SessionQualifiedName(cityPath, agent, rigs, "demo/ops.worker-1", ""); got != "demo/ops.worker-1" {
		t.Fatalf("SessionQualifiedName(rig-qualified binding) = %q, want %q", got, "demo/ops.worker-1")
	}
}

func TestCityNameFallsBackToCityDirBase(t *testing.T) {
	cityPath := filepath.Join(t.TempDir(), "city-root")
	got := CityName(cityPath, &config.City{})
	if got != "city-root" {
		t.Fatalf("CityName() = %q, want %q", got, "city-root")
	}
}

func TestResolveWorkDirPathStrictRejectsInvalidTemplate(t *testing.T) {
	cityPath := t.TempDir()
	_, err := ResolveWorkDirPathStrict(cityPath, "gastown", "demo/refinery", config.Agent{
		Name:    "refinery",
		Dir:     "demo",
		WorkDir: ".gc/worktrees/{{.RigName}}/refinery",
	}, []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}})
	if err == nil {
		t.Fatal("ResolveWorkDirPathStrict() error = nil, want invalid template error")
	}
}

func TestExpandCommandTemplateFallsBackToCityDirBase(t *testing.T) {
	cityPath := filepath.Join(t.TempDir(), "demo-city")
	agent := config.Agent{Name: "worker"}

	got, err := ExpandCommandTemplate("echo {{.CityName}}", cityPath, "", agent, nil)
	if err != nil {
		t.Fatalf("ExpandCommandTemplate() error = %v, want nil", err)
	}
	if got != "echo demo-city" {
		t.Fatalf("ExpandCommandTemplate() = %q, want %q", got, "echo demo-city")
	}
}

func TestConfiguredRigNameMatchesSymlinkAliasPath(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	rigPath := filepath.Join(realRoot, "demo")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(root, "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink setup unavailable: %v", err)
	}

	aliasRigPath := filepath.Join(aliasRoot, "demo")
	got := ConfiguredRigName(t.TempDir(), config.Agent{
		Name: "worker",
		Dir:  aliasRigPath,
	}, []config.Rig{{Name: "demo", Path: rigPath}})
	if got != "demo" {
		t.Fatalf("ConfiguredRigName() = %q, want %q", got, "demo")
	}
}

func TestSamePathUsesSharedPathNormalization(t *testing.T) {
	a := "/private/tmp/gc-home"
	b := "/tmp/gc-home"
	got := samePath(a, b)
	want := runtime.GOOS == "darwin"
	if got != want {
		t.Fatalf("samePath(%q, %q) = %v, want %v", a, b, got, want)
	}
}

// writeStaleGitPointer creates a stale worktree marker file at parent/.git
// whose gitdir target does not exist on disk. Mirrors the bug shape from
// gascity#1556.
func writeStaleGitPointer(t *testing.T, parent, missingTarget string) {
	t.Helper()
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(parent, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+missingTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeValidGitPointer creates a worktree marker file at parent/.git whose
// gitdir target is an existing directory.
func writeValidGitPointer(t *testing.T, parent, target string) {
	t.Helper()
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(parent, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+target+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAncestorWorktreesNotStale_NoMarkersInAncestry(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a", "b", "c", "polecat-1")
	if err := ValidateAncestorWorktreesNotStale(target); err != nil {
		t.Fatalf("ValidateAncestorWorktreesNotStale() = %v, want nil (ancestry has no .git markers)", err)
	}
}

func TestValidateAncestorWorktreesNotStale_AncestorHasRealGitDir(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, ".gc", "worktrees", "demo", "polecats", "polecat-1")
	if err := ValidateAncestorWorktreesNotStale(target); err != nil {
		t.Fatalf("ValidateAncestorWorktreesNotStale() = %v, want nil (ancestor has real .git directory)", err)
	}
}

func TestValidateAncestorWorktreesNotStale_AncestorHasValidWorktreePointer(t *testing.T) {
	root := t.TempDir()
	wtParent := filepath.Join(root, "repo", ".gc", "worktrees", "demo", "polecats", "furiosa")
	gitdirTarget := filepath.Join(root, "repo", ".git", "worktrees", "furiosa")
	writeValidGitPointer(t, wtParent, gitdirTarget)
	target := filepath.Join(wtParent, "child-spawn")
	if err := ValidateAncestorWorktreesNotStale(target); err != nil {
		t.Fatalf("ValidateAncestorWorktreesNotStale() = %v, want nil (ancestor has valid worktree pointer)", err)
	}
}

func TestValidateAncestorWorktreesNotStale_AncestorHasStalePointer(t *testing.T) {
	root := t.TempDir()
	staleParent := filepath.Join(root, "repo", ".gc", "worktrees", "demo", "polecats", "furiosa")
	missingTarget := filepath.Join(root, "repo", ".git", "worktrees", "furiosa-was-removed")
	writeStaleGitPointer(t, staleParent, missingTarget)
	target := filepath.Join(staleParent, "worktrees", "ta-2j4p")
	err := ValidateAncestorWorktreesNotStale(target)
	if err == nil {
		t.Fatal("ValidateAncestorWorktreesNotStale() = nil, want error (ancestor has stale worktree pointer)")
	}
	for _, want := range []string{staleParent, missingTarget} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing reference %q", err.Error(), want)
		}
	}
}

func TestValidateAncestorWorktreesNotStale_PathItselfMissingButAncestryClean(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "does", "not", "exist", "yet")
	if err := ValidateAncestorWorktreesNotStale(target); err != nil {
		t.Fatalf("ValidateAncestorWorktreesNotStale() = %v, want nil (path missing but ancestry clean)", err)
	}
}

func TestValidateAncestorWorktreesNotStale_StopsAtFirstValidMarker(t *testing.T) {
	// Two levels of worktree markers in the chain. The closer one (a valid
	// worktree pointer) should stop the walk; the further-up stale marker
	// must NOT be reached and thus must NOT trigger rejection. This mirrors
	// git's own resolution: once a valid worktree root is found, deeper
	// ancestors are not consulted.
	root := t.TempDir()
	deepStaleParent := filepath.Join(root, "outer")
	deepStaleTarget := filepath.Join(root, "outer", ".git", "worktrees", "gone")
	writeStaleGitPointer(t, deepStaleParent, deepStaleTarget)

	innerWtParent := filepath.Join(deepStaleParent, "inner", "polecats", "furiosa")
	innerWtTarget := filepath.Join(root, "real-repo", ".git", "worktrees", "furiosa")
	writeValidGitPointer(t, innerWtParent, innerWtTarget)

	target := filepath.Join(innerWtParent, "child-spawn")
	if err := ValidateAncestorWorktreesNotStale(target); err != nil {
		t.Fatalf("ValidateAncestorWorktreesNotStale() = %v, want nil (closer valid marker should stop the walk)", err)
	}
}

func TestValidateAncestorWorktreesNotStale_WalkTerminatesAtFilesystemRoot(t *testing.T) {
	// Tests that the loop terminates on systems where the temp tree has no
	// .git marker anywhere from path up to /. The validator must not loop
	// or stat-thrash.
	root := t.TempDir()
	target := filepath.Join(root, "no", "git", "anywhere", "in", "ancestry")
	if err := ValidateAncestorWorktreesNotStale(target); err != nil {
		t.Fatalf("ValidateAncestorWorktreesNotStale() = %v, want nil (no markers up to FS root)", err)
	}
}

// PR #2033 review: a gitdir: target written as a relative path must be
// resolved against the directory containing the .git file (Git's gitfile
// format), not against the process working directory.
func TestValidateAncestorWorktreesNotStale_RelativeGitdirTarget(t *testing.T) {
	root := t.TempDir()
	wtParent := filepath.Join(root, "repo", ".gc", "worktrees", "demo", "polecats", "furiosa")
	// Absolute target on disk lives alongside the pointer parent, so a
	// relative pointer "../../../.git/worktrees/furiosa" resolves to it.
	absTarget := filepath.Join(root, "repo", ".git", "worktrees", "furiosa")
	if err := os.MkdirAll(absTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wtParent, 0o755); err != nil {
		t.Fatal(err)
	}
	relTarget, err := filepath.Rel(wtParent, absTarget)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	gitFile := filepath.Join(wtParent, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+relTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(wtParent, "child-spawn")
	if err := ValidateAncestorWorktreesNotStale(target); err != nil {
		t.Fatalf("ValidateAncestorWorktreesNotStale() = %v, want nil (relative gitdir should resolve against .git parent)", err)
	}
}

// PR #2033 review: a gitdir target that exists on disk but is not a
// directory (regular file, broken symlink, etc.) must be rejected with
// the same shape as a missing target — the pointer is non-worktree-
// capable and spawning into a descendant produces dangling content.
func TestValidateAncestorWorktreesNotStale_GitdirTargetNotDirectory(t *testing.T) {
	root := t.TempDir()
	staleParent := filepath.Join(root, "repo", ".gc", "worktrees", "demo", "polecats", "furiosa")
	if err := os.MkdirAll(staleParent, 0o755); err != nil {
		t.Fatal(err)
	}
	// Target exists but is a regular file rather than a worktree admin dir.
	fileTarget := filepath.Join(root, "repo", ".git", "worktrees", "furiosa-file")
	if err := os.MkdirAll(filepath.Dir(fileTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileTarget, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFile := filepath.Join(staleParent, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+fileTarget+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(staleParent, "child-spawn")
	err := ValidateAncestorWorktreesNotStale(target)
	if err == nil {
		t.Fatal("ValidateAncestorWorktreesNotStale() = nil, want error (gitdir target is a file, not a directory)")
	}
	for _, want := range []string{staleParent, fileTarget, "not a directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing reference %q", err.Error(), want)
		}
	}
}

func TestResolveTmuxAlias_EmptyWhenUnset(t *testing.T) {
	cityPath := t.TempDir()
	got, err := ResolveTmuxAlias(cityPath, "gastown", config.Agent{Name: "worker", Dir: "demo"}, nil)
	if err != nil {
		t.Fatalf("ResolveTmuxAlias: %v", err)
	}
	if got != "" {
		t.Fatalf("ResolveTmuxAlias() = %q, want empty (no template configured)", got)
	}
}

func TestResolveTmuxAlias_ExpandsRigTemplate(t *testing.T) {
	cityPath := t.TempDir()
	rigs := []config.Rig{{Name: "demo", Path: filepath.Join(cityPath, "repos", "demo")}}
	got, err := ResolveTmuxAlias(cityPath, "gastown", config.Agent{
		Name:      "crew-demo",
		Dir:       "demo",
		TmuxAlias: "crew--{{.Rig}}",
	}, rigs)
	if err != nil {
		t.Fatalf("ResolveTmuxAlias: %v", err)
	}
	if got != "crew--demo" {
		t.Fatalf("ResolveTmuxAlias() = %q, want %q", got, "crew--demo")
	}
}

func TestResolveTmuxAlias_SanitizesQualifiedAgentName(t *testing.T) {
	cityPath := t.TempDir()
	got, err := ResolveTmuxAlias(cityPath, "gastown", config.Agent{
		Name:        "mayor",
		BindingName: "gastown",
		TmuxAlias:   "{{.Agent}}",
	}, nil)
	if err != nil {
		t.Fatalf("ResolveTmuxAlias: %v", err)
	}
	// "gastown.mayor" must be sanitized to "gastown__mayor" for tmux.
	if got != "gastown__mayor" {
		t.Fatalf("ResolveTmuxAlias() = %q, want %q", got, "gastown__mayor")
	}
}

func TestResolveTmuxAlias_ReturnsErrorOnBadTemplate(t *testing.T) {
	_, err := ResolveTmuxAlias("", "", config.Agent{
		Name:      "worker",
		TmuxAlias: "{{.NotAField}}",
	}, nil)
	if err == nil {
		t.Fatal("ResolveTmuxAlias: want error on unknown template field, got nil")
	}
}

// TestPathContextRigScopedAgentResolvesRigFromQualifiedNamePrefix covers
// gascity#2070: a scope="rig" agent whose Dir is not stamped (or points
// outside any configured rig path) must still resolve its rig — and thus
// GC_RIG/GC_RIG_ROOT — from the qualified-name prefix. Without this, the
// rig keys leak through as empty values.
func TestPathContextRigScopedAgentResolvesRigFromQualifiedNamePrefix(t *testing.T) {
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "thriva")
	rigs := []config.Rig{{Name: "thriva", Path: rigPath}}
	// No Dir stamp; the rig association lives only in the qualified name.
	a := config.Agent{Name: "my_impl", Scope: "rig", WorkDir: ".gc/worktrees/my_impl"}

	ctx := PathContextForQualifiedName(cityPath, "city", "thriva/my_impl", a, rigs)
	if ctx.Rig != "thriva" {
		t.Fatalf("ctx.Rig = %q, want %q", ctx.Rig, "thriva")
	}
	if ctx.RigRoot != rigPath {
		t.Fatalf("ctx.RigRoot = %q, want %q", ctx.RigRoot, rigPath)
	}
}

// TestPathContextNonRigScopedAgentDoesNotInferRig guards against false
// positives: a non-rig-scoped agent must not be assigned a rig from a
// coincidental qualified-name prefix.
func TestPathContextNonRigScopedAgentDoesNotInferRig(t *testing.T) {
	cityPath := t.TempDir()
	rigs := []config.Rig{{Name: "thriva", Path: filepath.Join(cityPath, "rigs", "thriva")}}
	a := config.Agent{Name: "my_impl", Scope: "city"}

	ctx := PathContextForQualifiedName(cityPath, "city", "thriva/my_impl", a, rigs)
	if ctx.Rig != "" {
		t.Fatalf("ctx.Rig = %q, want empty for city-scoped agent", ctx.Rig)
	}
}

// TestPathContextRigScopedAgentPrefersStampedDir confirms the existing
// dir-based association still wins when Dir is stamped (no regression of
// the working path).
func TestPathContextRigScopedAgentPrefersStampedDir(t *testing.T) {
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "thriva")
	rigs := []config.Rig{{Name: "thriva", Path: rigPath}}
	a := config.Agent{Name: "my_impl", Dir: "thriva", Scope: "rig"}

	ctx := PathContextForQualifiedName(cityPath, "city", "thriva/my_impl", a, rigs)
	if ctx.Rig != "thriva" {
		t.Fatalf("ctx.Rig = %q, want %q", ctx.Rig, "thriva")
	}
	if ctx.RigRoot != rigPath {
		t.Fatalf("ctx.RigRoot = %q, want %q", ctx.RigRoot, rigPath)
	}
}

// The nesting guard: a session worktree must never be created inside another
// checkout that lives under the worktrees root (vn-rm9u8g).

// townPaths returns the two paths every nesting test needs: the worktrees root
// a city hands session trees out of, and one polecat slot path under it.
func townPaths(t *testing.T) (worktreesRoot, slot string) {
	t.Helper()
	city := t.TempDir()
	worktreesRoot = filepath.Join(city, ".gc", "worktrees")
	slot = filepath.Join(worktreesRoot, "demo", "polecats", "polecat-opus-high", "furiosa")
	return worktreesRoot, slot
}

func TestValidateNotNestedInSessionWorktree_CleanAncestryIsAllowed(t *testing.T) {
	worktreesRoot, slot := townPaths(t)
	if err := ValidateNotNestedInSessionWorktree(slot, worktreesRoot); err != nil {
		t.Fatalf("ValidateNotNestedInSessionWorktree() = %v, want nil (no ancestor holds a checkout)", err)
	}
}

func TestValidateNotNestedInSessionWorktree_TargetItselfIsAWorktree(t *testing.T) {
	// The normal case, and the one a wrong guard would break: the slot was
	// prebuilt, so the path itself IS a worktree. Only ancestors are judged.
	worktreesRoot, slot := townPaths(t)
	writeValidGitPointer(t, slot, filepath.Join(t.TempDir(), "rig", ".git", "worktrees", "furiosa"))
	if err := ValidateNotNestedInSessionWorktree(slot, worktreesRoot); err != nil {
		t.Fatalf("ValidateNotNestedInSessionWorktree() = %v, want nil (the spawn target is allowed to be a worktree)", err)
	}
}

func TestValidateNotNestedInSessionWorktree_AncestorIsALinkedWorktree(t *testing.T) {
	// The measured shape: worktrees created inside a polecat slot that is
	// already a checkout, so both paths answer as one tree.
	worktreesRoot, slot := townPaths(t)
	writeValidGitPointer(t, slot, filepath.Join(t.TempDir(), "rig", ".git", "worktrees", "furiosa"))
	// A second agent home landing inside the first one: the measured shape,
	// where one leaf name resolved into two sessions sharing a tree.
	nested := filepath.Join(slot, "polecat-opus-high", "nux")
	err := ValidateNotNestedInSessionWorktree(nested, worktreesRoot)
	if err == nil {
		t.Fatal("ValidateNotNestedInSessionWorktree() = nil, want error (spawn target is inside an existing worktree)")
	}
	for _, want := range []string{nested, slot} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing reference %q", err.Error(), want)
		}
	}
}

func TestValidateNotNestedInSessionWorktree_PerBeadWorktreeUnderAnAgentHomeIsAllowed(t *testing.T) {
	// The sanctioned exception. The core polecat formula creates
	// "$(pwd)/worktrees/$WORK_BEAD_ID" inside the agent home, and the bead
	// worktree reaper collects them there. Refusing this shape would break
	// per-bead worktrees in every town that uses them.
	worktreesRoot, slot := townPaths(t)
	writeValidGitPointer(t, slot, filepath.Join(t.TempDir(), "rig", ".git", "worktrees", "furiosa"))
	perBead := filepath.Join(slot, "worktrees", "vn-4b67ts")
	if err := ValidateNotNestedInSessionWorktree(perBead, worktreesRoot); err != nil {
		t.Fatalf("ValidateNotNestedInSessionWorktree() = %v, want nil (per-bead worktrees live under <home>/worktrees/<bead-id>)", err)
	}
}

func TestValidateNotNestedInSessionWorktree_AncestorIsAWholeRepository(t *testing.T) {
	worktreesRoot, slot := townPaths(t)
	if err := os.MkdirAll(filepath.Join(slot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(slot, "polecat-opus-high", "nux")
	err := ValidateNotNestedInSessionWorktree(nested, worktreesRoot)
	if err == nil {
		t.Fatal("ValidateNotNestedInSessionWorktree() = nil, want error (spawn target is inside a cloned repository)")
	}
	if !strings.Contains(err.Error(), slot) {
		t.Errorf("error %q does not name the repository at %q", err.Error(), slot)
	}
}

func TestValidateNotNestedInSessionWorktree_CheckoutAboveTheWorktreesRootIsAllowed(t *testing.T) {
	// The brick test. The city checkout holds .gc/worktrees, and a city that
	// is itself a git worktree has a .git FILE at its root. If the walk went
	// above the worktrees root, every spawn in that town would be refused.
	city := t.TempDir()
	writeValidGitPointer(t, city, filepath.Join(t.TempDir(), "city-repo", ".git", "worktrees", "town"))
	worktreesRoot := filepath.Join(city, ".gc", "worktrees")
	slot := filepath.Join(worktreesRoot, "demo", "polecats", "polecat-opus-high", "furiosa")
	if err := ValidateNotNestedInSessionWorktree(slot, worktreesRoot); err != nil {
		t.Fatalf("ValidateNotNestedInSessionWorktree() = %v, want nil (a checkout above the worktrees root is normal)", err)
	}
}

func TestValidateNotNestedInSessionWorktree_WorktreesRootItselfIsNotJudged(t *testing.T) {
	// A marker ON the worktrees root is above the bound, not under it.
	city := t.TempDir()
	worktreesRoot := filepath.Join(city, ".gc", "worktrees")
	writeValidGitPointer(t, worktreesRoot, filepath.Join(t.TempDir(), "repo", ".git", "worktrees", "wt"))
	slot := filepath.Join(worktreesRoot, "demo", "polecats", "furiosa")
	if err := ValidateNotNestedInSessionWorktree(slot, worktreesRoot); err != nil {
		t.Fatalf("ValidateNotNestedInSessionWorktree() = %v, want nil (the worktrees root is the bound, not an ancestor under it)", err)
	}
}

func TestValidateNotNestedInSessionWorktree_MissingArgsAreNoOps(t *testing.T) {
	worktreesRoot, slot := townPaths(t)
	if err := ValidateNotNestedInSessionWorktree("", worktreesRoot); err != nil {
		t.Errorf("ValidateNotNestedInSessionWorktree(\"\", root) = %v, want nil", err)
	}
	if err := ValidateNotNestedInSessionWorktree(slot, ""); err != nil {
		t.Errorf("ValidateNotNestedInSessionWorktree(slot, \"\") = %v, want nil (no bound means nothing to judge)", err)
	}
}

func TestValidateNotNestedInSessionWorktree_PathOutsideTheWorktreesRoot(t *testing.T) {
	// An agent whose work_dir is the rig root, which is nowhere near the
	// worktrees root. There is no ancestor under the bound, so nothing to say.
	worktreesRoot, _ := townPaths(t)
	outside := filepath.Join(t.TempDir(), "rigs", "vessel-network")
	if err := ValidateNotNestedInSessionWorktree(outside, worktreesRoot); err != nil {
		t.Fatalf("ValidateNotNestedInSessionWorktree() = %v, want nil (path is not under the worktrees root)", err)
	}
}

func TestValidateSpawnTarget_RunsBothHalves(t *testing.T) {
	// One door, both guards. A spawn path that wires only the stale half is
	// the hole this bead closed, so prove the wrapper catches each fault.
	staleRoot := t.TempDir()
	staleWorktrees := filepath.Join(staleRoot, ".gc", "worktrees")
	staleSlot := filepath.Join(staleWorktrees, "demo", "polecats", "furiosa")
	writeStaleGitPointer(t, staleSlot, filepath.Join(staleRoot, "repo", ".git", "worktrees", "gone"))
	if err := ValidateSpawnTarget(filepath.Join(staleSlot, "child"), staleWorktrees); err == nil {
		t.Error("ValidateSpawnTarget() = nil on a stale ancestor pointer, want error")
	}

	liveRoot := t.TempDir()
	liveWorktrees := filepath.Join(liveRoot, ".gc", "worktrees")
	liveSlot := filepath.Join(liveWorktrees, "demo", "polecats", "furiosa")
	writeValidGitPointer(t, liveSlot, filepath.Join(liveRoot, "repo", ".git", "worktrees", "furiosa"))
	if err := ValidateSpawnTarget(filepath.Join(liveSlot, "child"), liveWorktrees); err == nil {
		t.Error("ValidateSpawnTarget() = nil on a healthy nested ancestor, want error")
	}

	cleanRoot := t.TempDir()
	cleanWorktrees := filepath.Join(cleanRoot, ".gc", "worktrees")
	cleanSlot := filepath.Join(cleanWorktrees, "demo", "polecats", "furiosa")
	if err := ValidateSpawnTarget(cleanSlot, cleanWorktrees); err != nil {
		t.Errorf("ValidateSpawnTarget() = %v on a clean ancestry, want nil", err)
	}
}
