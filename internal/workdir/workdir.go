// Package workdir resolves agent working directories from config templates.
package workdir

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/pathutil"
)

// PathContext holds template variables for work_dir expansion.
type PathContext struct {
	Agent         string
	AgentBase     string
	Rig           string
	RigRoot       string
	CityRoot      string
	CityName      string
	WorktreesRoot string
}

// CityName returns the effective workspace name for workdir/template expansion.
func CityName(cityPath string, cfg *config.City) string {
	return config.EffectiveCityName(cfg, filepath.Base(filepath.Clean(cityPath)))
}

// ResolveDirPath returns an absolute path for dir, resolving relative paths
// against the city root.
func ResolveDirPath(cityPath, dir string) string {
	if dir == "" {
		return cityPath
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(cityPath, dir)
}

// ConfiguredRigName returns the rig associated with an agent, preferring the
// legacy dir-as-rig convention and falling back to path matching.
func ConfiguredRigName(cityPath string, a config.Agent, rigs []config.Rig) string {
	if a.Dir == "" {
		return ""
	}
	for _, rig := range rigs {
		if a.Dir == rig.Name {
			return rig.Name
		}
	}
	abs := ResolveDirPath(cityPath, a.Dir)
	for _, rig := range rigs {
		if samePath(abs, rig.Path) {
			return rig.Name
		}
	}
	return ""
}

// RigRootForName returns the configured root path for rigName.
func RigRootForName(rigName string, rigs []config.Rig) string {
	for _, rig := range rigs {
		if rig.Name == rigName {
			return rig.Path
		}
	}
	return ""
}

// WorktreesRoot returns the configured root for session worktrees.
func WorktreesRoot(cityPath string) string {
	for _, key := range []string{"GC_WORKTREES_DIR", "T3CODE_WORKTREES_DIR"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	if t3Home := strings.TrimSpace(os.Getenv("T3CODE_HOME")); t3Home != "" {
		return filepath.Join(t3Home, "worktrees")
	}
	return filepath.Join(cityPath, ".gc", "worktrees")
}

// rigNameForQualifiedAgent resolves the rig an agent belongs to. It prefers
// the dir-based association used by ConfiguredRigName, then falls back to the
// qualified-name prefix for explicitly rig-scoped agents whose Dir is not
// stamped or points outside any configured rig path. This keeps
// GC_RIG/GC_RIG_ROOT populated for rig-scoped agents whose work_dir lands
// outside the rig filesystem (e.g. a city-level worktree), where the
// dir heuristic alone returns "" and the rig keys would otherwise leak
// through as empty values — gascity#2070.
func rigNameForQualifiedAgent(cityPath, qualifiedName string, a config.Agent, rigs []config.Rig) string {
	if name := ConfiguredRigName(cityPath, a, rigs); name != "" {
		return name
	}
	if a.Scope != "rig" {
		return ""
	}
	dir, _ := config.ParseQualifiedName(qualifiedName)
	if dir == "" {
		return ""
	}
	for _, rig := range rigs {
		if dir == rig.Name {
			return rig.Name
		}
	}
	return ""
}

// PathContextForQualifiedName builds template context for work_dir expansion.
func PathContextForQualifiedName(cityPath, cityName, qualifiedName string, a config.Agent, rigs []config.Rig) PathContext {
	rigName := rigNameForQualifiedAgent(cityPath, qualifiedName, a, rigs)
	_, agentBase := config.ParseQualifiedName(qualifiedName)
	return PathContext{
		Agent:         qualifiedName,
		AgentBase:     agentBase,
		Rig:           rigName,
		RigRoot:       RigRootForName(rigName, rigs),
		CityRoot:      cityPath,
		CityName:      cityName,
		WorktreesRoot: WorktreesRoot(cityPath),
	}
}

// ExpandCommandTemplate renders command using the same PathContext surface as
// work_dir and session_setup templates. When cityName is empty, it falls back
// to the city directory basename so callers don't have to duplicate that logic.
func ExpandCommandTemplate(command, cityPath, cityName string, a config.Agent, rigs []config.Rig) (string, error) {
	if command == "" || !strings.Contains(command, "{{") {
		return command, nil
	}
	if strings.TrimSpace(cityName) == "" {
		cityName = filepath.Base(filepath.Clean(cityPath))
	}
	ctx := PathContextForQualifiedName(cityPath, cityName, a.QualifiedName(), a, rigs)
	return ExpandTemplateStrict(command, ctx)
}

// SessionQualifiedName returns the canonical work_dir identity for a concrete
// session instance. Single-session agents keep their template identity unless
// an explicit name, such as a resolved tmux_alias, supplies the concrete
// session identity; pooled agents use the alias or generated explicit name.
func SessionQualifiedName(cityPath string, a config.Agent, rigs []config.Rig, alias, explicitName string) string {
	if !a.SupportsMultipleSessions() {
		if strings.TrimSpace(alias) == "" {
			if qualified := sessionQualifiedNameFromIdentity(cityPath, a, rigs, explicitName); qualified != "" {
				return qualified
			}
		}
		return a.QualifiedName()
	}
	identity := strings.TrimSpace(alias)
	if identity == "" {
		identity = strings.TrimSpace(explicitName)
	}
	if qualified := sessionQualifiedNameFromIdentity(cityPath, a, rigs, identity); qualified != "" {
		return qualified
	}
	return a.QualifiedName()
}

func sessionQualifiedNameFromIdentity(cityPath string, a config.Agent, rigs []config.Rig, identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}

	_, instanceName := config.ParseQualifiedName(identity)
	if instanceName != "" {
		identity = instanceName
	}
	if a.BindingName != "" {
		prefix := a.BindingName + "."
		identity = strings.TrimPrefix(identity, prefix)
	}

	qualified := a.QualifiedInstanceName(identity)
	rigName := ConfiguredRigName(cityPath, a, rigs)
	if rigName == "" {
		return qualified
	}
	_, agentBase := config.ParseQualifiedName(qualified)
	return rigName + "/" + agentBase
}

// ExpandTemplateStrict expands Go text/template placeholders in a work_dir
// string and returns an error when parsing or execution fails.
func ExpandTemplateStrict(spec string, ctx PathContext) (string, error) {
	if spec == "" || !strings.Contains(spec, "{{") {
		return spec, nil
	}
	tmpl, err := template.New("workdir").Option("missingkey=error").Parse(spec)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ExpandTemplate expands Go text/template placeholders in a work_dir string.
// On parse or execute error, the raw string is returned.
func ExpandTemplate(spec string, ctx PathContext) string {
	expanded, err := ExpandTemplateStrict(spec, ctx)
	if err != nil {
		return spec
	}
	return expanded
}

// ResolveTmuxAlias expands the agent's tmux_alias template and sanitizes the
// result for use as a tmux session name. Returns "" with no error when the
// agent has no tmux_alias configured. The returned name is suitable for use
// as a session bead's session_name metadata.
func ResolveTmuxAlias(cityPath, cityName string, a config.Agent, rigs []config.Rig) (string, error) {
	spec := strings.TrimSpace(a.TmuxAlias)
	if spec == "" {
		return "", nil
	}
	ctx := PathContextForQualifiedName(cityPath, cityName, a.QualifiedName(), a, rigs)
	expanded, err := ExpandTemplateStrict(spec, ctx)
	if err != nil {
		return "", fmt.Errorf("expanding tmux_alias %q: %w", spec, err)
	}
	resolved := strings.TrimSpace(expanded)
	if resolved == "" {
		return "", nil
	}
	return agent.SanitizeQualifiedNameForSession(resolved), nil
}

// ResolveWorkDirPathStrict returns the effective session working directory and
// surfaces work_dir template errors to callers that need to fail closed.
func ResolveWorkDirPathStrict(cityPath, cityName, qualifiedName string, a config.Agent, rigs []config.Rig) (string, error) {
	if a.WorkDir == "" {
		if rigName := ConfiguredRigName(cityPath, a, rigs); rigName != "" {
			if rigRoot := RigRootForName(rigName, rigs); rigRoot != "" {
				return ResolveDirPath(cityPath, rigRoot), nil
			}
		}
		return ResolveDirPath(cityPath, a.Dir), nil
	}
	ctx := PathContextForQualifiedName(cityPath, cityName, qualifiedName, a, rigs)
	expanded, err := ExpandTemplateStrict(a.WorkDir, ctx)
	if err != nil {
		return "", fmt.Errorf("expand work_dir %q: %w", a.WorkDir, err)
	}
	return ResolveDirPath(cityPath, expanded), nil
}

// ResolveWorkDirPath returns the effective session working directory for an
// agent. When work_dir is unset, rig-scoped agents continue to use their rig
// root for backward compatibility.
func ResolveWorkDirPath(cityPath, cityName, qualifiedName string, a config.Agent, rigs []config.Rig) string {
	path, err := ResolveWorkDirPathStrict(cityPath, cityName, qualifiedName, a, rigs)
	if err != nil {
		ctx := PathContextForQualifiedName(cityPath, cityName, qualifiedName, a, rigs)
		return ResolveDirPath(cityPath, ExpandTemplate(a.WorkDir, ctx))
	}
	return path
}

func samePath(a, b string) bool {
	return pathutil.SamePath(a, b)
}

// ValidateAncestorWorktreesNotStale walks path's ancestor chain and returns
// an error when any ancestor has a regular-file ".git" worktree pointer
// whose "gitdir:" target is unusable. The walk stops as soon as it
// encounters a ".git" marker — any regular file (with or without a usable
// gitdir pointer) or a real ".git" directory. Reaching the filesystem
// root without finding a marker is not an error.
//
// Failure modes that fail closed (the gitdir pointer is present and parses):
//   - the gitdir target doesn't exist on disk
//   - the gitdir target exists but is not a directory (non-worktree-
//     capable, e.g. a regular file at the expected admin-dir path)
//
// Failure modes that fail open (the .git file is present but not a
// recognizable worktree pointer): unreadable file, missing "gitdir:"
// prefix. In both cases the walk stops — anything further up belongs
// to the surrounding repository, and fail-closed here would block
// legitimate spawns whenever an unrelated ancestor .git file is
// permission-restricted or non-pointer.
//
// Relative gitdir targets are resolved against the directory holding
// the ".git" file (Git's gitfile format), matching Git's own
// interpretation rather than the process working directory.
//
// This is the spawn-time guard for gascity#1556: a stale worktree pointer
// on an ancestor lets "git -C <rig-root> worktree add <child>" register a
// structurally orphaned child that can't be reached from the ancestor
// itself. Failing closed on stale-pointer cases before invoking "git
// worktree add" surfaces the stale ancestor to the operator instead of
// producing dangling content.
func ValidateAncestorWorktreesNotStale(path string) error {
	// Walk from path's parent upward. The spawn target itself may not yet
	// exist (we are typically about to MkdirAll it); only ancestors are
	// inspected.
	cur := filepath.Dir(filepath.Clean(path))
	for {
		gitPath := filepath.Join(cur, ".git")
		info, err := os.Lstat(gitPath)
		if err == nil {
			if info.Mode().IsRegular() {
				data, rerr := os.ReadFile(gitPath)
				if rerr == nil {
					content := strings.TrimSpace(string(data))
					if strings.HasPrefix(content, "gitdir:") {
						target := strings.TrimSpace(strings.TrimPrefix(content, "gitdir:"))
						// Git's gitfile format: a relative gitdir target
						// is resolved against the directory containing
						// the .git file, not the process working
						// directory.
						if !filepath.IsAbs(target) {
							target = filepath.Join(cur, target)
						}
						target = filepath.Clean(target)
						targetInfo, terr := os.Stat(target)
						if terr != nil {
							return fmt.Errorf(
								"worktree spawn rejected: ancestor %q has stale .git pointer (gitdir target %q does not exist): %w",
								cur, target, terr)
						}
						if !targetInfo.IsDir() {
							return fmt.Errorf(
								"worktree spawn rejected: ancestor %q has stale .git pointer (gitdir target %q is not a directory)",
								cur, target)
						}
					}
				}
				// Either a recognizable worktree pointer with a usable
				// target, or a .git file we couldn't parse (unreadable
				// or missing the "gitdir:" prefix). Either way we stop
				// the walk — anything further up belongs to the
				// surrounding repository and is git's responsibility,
				// not ours.
				return nil
			}
			if info.IsDir() {
				// Reached a real .git directory (main repo root). Stop.
				return nil
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding a marker.
			return nil
		}
		cur = parent
	}
}

// ValidateNotNestedInSessionWorktree returns an error when path would be
// created INSIDE another agent's checkout under worktreesRoot. It is the other
// half of the spawn-time guard: ValidateAncestorWorktreesNotStale catches an
// ancestor whose worktree pointer is BROKEN, and this catches an ancestor that
// is a perfectly healthy checkout, which the stale walk stops on and allows.
//
// WHY A HEALTHY ANCESTOR IS ALSO WRONG. "git worktree add" at a path inside an
// existing worktree registers the child through its parent, and from then on
// both paths answer as one checkout. Two sessions handed those two paths are
// two sessions in one tree: they fight over HEAD, and a setup step that resets
// the tree onto its own branch deletes the other session's uncommitted work.
// That happened twice in fifteen minutes on 2026-08-23 and took the only copy
// of two beads' work with it (vn-rm9u8g, from vn-equwvo).
//
// THE PER-BEAD LAYOUT IS EXEMPT, AND MUST STAY EXEMPT. A "worktrees" directory
// inside an agent home is the sanctioned place for per-bead worktrees: the core
// polecat formula creates "$(pwd)/worktrees/$WORK_BEAD_ID" itself, and
// reapClosedBeadWorktrees collects them there. So a target whose path reaches
// its checkout ancestor through a directory named "worktrees" is allowed. Only
// OTHER nesting is refused, which is the shape that came from one leaf name
// resolving into two sessions.
//
// THE BOUND IS DELIBERATE. The walk only inspects ancestors strictly under
// worktreesRoot, the directory the city hands out session worktrees from. Above
// that line a checkout is normal and expected: the city checkout itself holds
// .gc/worktrees, and a city that is ITSELF a git worktree would otherwise make
// every spawn in town illegal.
//
// The spawn target itself is not inspected. A session worktree IS a checkout;
// that is the point. Only its ancestors are.
//
// Both a ".git" file (a linked worktree) and a real ".git" directory (a whole
// repository someone cloned into a slot) are refused, because a worktree added
// underneath either one lands inside a tree that already belongs to somebody.
func ValidateNotNestedInSessionWorktree(path, worktreesRoot string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(worktreesRoot) == "" {
		return nil
	}
	root := filepath.Clean(worktreesRoot)
	cur := filepath.Dir(filepath.Clean(path))
	// True once the walk has passed a "worktrees" directory on its way up, so
	// a checkout found above it is the per-bead layout described above.
	viaPerBeadDir := false
	for pathIsUnder(cur, root) {
		gitPath := filepath.Join(cur, ".git")
		info, err := os.Lstat(gitPath)
		if err == nil && (info.Mode().IsRegular() || info.IsDir()) {
			if viaPerBeadDir {
				return nil
			}
			shape := "that directory has a .git file, so it is a git worktree"
			if info.IsDir() {
				shape = "that directory has a real .git directory, so it is a whole repository"
			}
			return fmt.Errorf(
				"worktree spawn rejected: %q is inside the checkout at %q (%s). "+
					"A worktree created inside another worktree resolves through its parent, so both paths become one tree and two sessions overwrite each other. "+
					"Move this agent's work_dir out of %q, or remove that stray checkout. "+
					"(A per-bead worktree under %q/worktrees/<bead-id> is the sanctioned exception and is not affected.)",
				path, cur, shape, cur, cur)
		}
		if filepath.Base(cur) == perBeadWorktreesDir {
			viaPerBeadDir = true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
	return nil
}

// perBeadWorktreesDir is the directory name an agent puts its per-bead
// worktrees in, inside its own home. The core polecat formula writes
// "$(pwd)/worktrees/$WORK_BEAD_ID" and reapClosedBeadWorktrees reads it back.
const perBeadWorktreesDir = "worktrees"

// pathIsUnder reports whether path sits strictly below root. Equal paths and
// anything outside root return false, so a walk bounded by it never leaves the
// directory root names.
func pathIsUnder(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ValidateSpawnTarget runs every check that must pass before a session working
// directory is created. Call this, never one half on its own: a spawn path that
// wires only the stale-pointer walk is the hole this guard already fell into
// once, because a healthy nested ancestor sails straight through that walk.
//
// worktreesRoot bounds the nesting half; pass WorktreesRoot(cityPath).
func ValidateSpawnTarget(path, worktreesRoot string) error {
	if err := ValidateAncestorWorktreesNotStale(path); err != nil {
		return err
	}
	return ValidateNotNestedInSessionWorktree(path, worktreesRoot)
}
