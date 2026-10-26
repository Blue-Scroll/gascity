package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gastownhall/gascity/internal/fsys"
)

// The bd pin needs one table of the city config: workspace.env. Reading it
// used to load the whole config (every include, pack and namepool) on every
// bd runtime env build, which means every exec order and every store opened
// through bdRuntimeEnv. On 2026-10-26 a supervisor at 356% CPU spent 208
// samples of a 3-second sample in that parse for the order dispatcher alone
// (vn-cq6w8n8).
//
// The table only changes when a config file changes, so it is memoised per
// city against a stamp over every file the load read (Provenance.Sources), in
// the shape proxiedScopeRuntimeEnvStamp uses. An edit to any of them, in a
// long-lived supervisor too, makes the next call load again.
var (
	workspaceEnvCache sync.Map // normalized city path → workspaceEnvEntry
	// workspaceEnvLoads counts cache misses so a test can prove the config is
	// parsed once rather than per call.
	workspaceEnvLoads atomic.Int64
)

type workspaceEnvEntry struct {
	// sources is every config file the load read, root first.
	sources []string
	// stamp is fileSetStamp(sources) as read BEFORE the load. Empty
	// means "not yet trusted": it never equals a real stamp.
	stamp string
	// env is workspace.env as written, before expandEnvMap. Expansion reads
	// the process environment, so it runs on every call, not once.
	env map[string]string
}

// cityWorkspaceEnv returns a private copy of the city's workspace.env as
// written in its config, before env expansion. Callers may mutate it.
func cityWorkspaceEnv(cityPath string) (map[string]string, error) {
	key := normalizePathForCompare(cityPath)
	sources := []string{filepath.Join(cityPath, "city.toml")}
	if value, ok := workspaceEnvCache.Load(key); ok {
		entry := value.(workspaceEnvEntry)
		if entry.stamp != "" && fileSetStamp(entry.sources) == entry.stamp {
			return maps.Clone(entry.env), nil
		}
		sources = entry.sources
	}
	// Stamp before loading, as cachedProxiedScopeRuntimeEnv does. A rewrite
	// that lands during the load then leaves an already-stale stamp, and the
	// next call loads again instead of serving old content under the new
	// file identity.
	before := fileSetStamp(sources)
	workspaceEnvLoads.Add(1)
	// The no-refresh loader on purpose: the full loader rewrites the managed
	// provider shim as a side effect (see workspacePinnedBdBinaryOptional).
	cfg, prov, err := loadPrematerializedCityConfigWithProvenance(fsys.OSFS{}, filepath.Join(cityPath, "city.toml"), skipRevisionSnapshot, io.Discard)
	if err != nil {
		workspaceEnvCache.Delete(key)
		return nil, err
	}
	entry := workspaceEnvEntry{sources: slices.Clone(prov.Sources), env: maps.Clone(cfg.Workspace.Env)}
	// The stamp is only trusted when it covered the files this load read. On
	// the first load, or after an include was added, it did not, so the next
	// call loads once more and stamps the right list.
	if slices.Equal(sources, prov.Sources) {
		entry.stamp = before
	}
	workspaceEnvCache.Store(key, entry)
	return maps.Clone(entry.env), nil
}

// fileSetStamp is the size and mtime of each path, in order. A missing file
// stamps as "-", so creating or deleting one changes the stamp too.
func fileSetStamp(paths []string) string {
	var stamp strings.Builder
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			stamp.WriteString("-|")
			continue
		}
		fmt.Fprintf(&stamp, "%d.%d|", info.Size(), info.ModTime().UnixNano())
	}
	return stamp.String()
}
