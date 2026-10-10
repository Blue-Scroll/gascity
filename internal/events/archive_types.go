package events

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// The type set of an archive is every event type it holds. A typed
// newest-first read (GET /events?type=X) skips an archive whose set lacks X
// without opening it, so a type that is rare in recent history does not gunzip
// the whole 1.3 GB of the town Mac's archives (vn-0o1qyss).
//
// An archive never changes once it is written, so its set never changes
// either. It is kept in two places:
//
//   - On disk, in a sidecar beside the archive: <archive>.types. The rotation
//     writes it while it gzips the archive, since it already reads every line
//     to do that (gzipAndArchive). An archive written before sidecars existed
//     gets one the first time the writer's FileRecorder.ListNewest (the
//     /events page) goes all the way through it. So even the first typed read
//     after a gc start skips (vn-3hx5swr).
//   - In memory, archiveTypes, so each sidecar is read once per process.
//
// The skip is only ever a shortcut. A sidecar that is missing, unreadable,
// malformed, or written for another file under the same name (its size or
// modification time differ) means "read the archive", never "skip it". The
// worst a bad sidecar can cost is time.
//
// The sidecar name ends in .types, so every scanner of the events directory
// that looks for an archive (".gz" suffix), a rotating file or a .gz.tmp
// passes it by. Retention deletes it with its archive (reapExpiredArchives),
// and the startup reaper deletes one whose archive is gone
// (reapTypesSidecars).

// typesSidecarSuffix is appended to the archive's own basename.
const typesSidecarSuffix = ".types"

// typesSidecarVersion is the only sidecar version a reader trusts. A sidecar
// with any other version is read past, as if missing.
const typesSidecarVersion = 1

// typesSidecarMaxBytes bounds a sidecar read. The town Mac's archives hold
// about 150 types each, a few KB; anything this large is not ours.
const typesSidecarMaxBytes = 1 << 20

// typesSidecar is the sidecar's JSON. ArchiveSize and ArchiveModNano name the
// exact archive file it was written for.
type typesSidecar struct {
	Version        int      `json:"v"`
	ArchiveSize    int64    `json:"archive_size"`
	ArchiveModNano int64    `json:"archive_mtime_ns"`
	Types          []string `json:"types"`
}

// typesSidecarPath is the sidecar beside the archive at archivePath.
func typesSidecarPath(archivePath string) string {
	return archivePath + typesSidecarSuffix
}

// isTypesSidecarBasename reports whether name is an archive's type sidecar.
func isTypesSidecarBasename(name string) bool {
	return strings.HasSuffix(name, typesSidecarSuffix) &&
		isCanonicalArchiveBasename(strings.TrimSuffix(name, typesSidecarSuffix))
}

// isTypesSidecarTmpBasename reports whether name is a sidecar write that
// never reached its rename: <archive>.types.<random>.tmp.
func isTypesSidecarTmpBasename(name string) bool {
	if !strings.HasPrefix(name, archivePrefix) || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	return strings.Contains(name, ".gz"+typesSidecarSuffix+".")
}

// lineEventType is the type a newest-first read finds on one archive line, the
// same way scanSegmentNewest finds it: the line head when FileRecorder wrote
// the line, else a full decode. ok is false for a line no read can decode, and
// such a line can match no filter. The set it builds may hold more than a read
// can match, never less, so a skip on it never drops a match.
func lineEventType(line []byte) (typ []byte, ok bool) {
	if _, head, headOK := lineHead(line); headOK {
		return head, true
	}
	var e Event
	if json.Unmarshal(trimLine(line), &e) != nil {
		return nil, false
	}
	return []byte(e.Type), true
}

// writeTypesSidecar writes the sidecar for the archive key names. The write is
// a temp file and a rename, so a reader sees a whole sidecar or none.
func writeTypesSidecar(key archiveTypesKey, types map[string]struct{}) error {
	sc := typesSidecar{
		Version:        typesSidecarVersion,
		ArchiveSize:    key.size,
		ArchiveModNano: key.modNano,
		Types:          make([]string, 0, len(types)),
	}
	for typ := range types {
		sc.Types = append(sc.Types, typ)
	}
	sort.Strings(sc.Types)
	body, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	dest := typesSidecarPath(key.path)
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// readTypesSidecar returns the type set the sidecar records for the archive
// key names. ok is false when there is no sidecar, it cannot be read or
// parsed, or it was written for a different file under the archive's name.
func readTypesSidecar(key archiveTypesKey) (map[string]struct{}, bool) {
	f, err := os.Open(typesSidecarPath(key.path))
	if err != nil {
		return nil, false
	}
	defer f.Close() //nolint:errcheck // read-only file
	body, err := io.ReadAll(io.LimitReader(f, typesSidecarMaxBytes+1))
	if err != nil || len(body) > typesSidecarMaxBytes {
		return nil, false
	}
	var sc typesSidecar
	if json.Unmarshal(body, &sc) != nil {
		return nil, false
	}
	if sc.Version != typesSidecarVersion || sc.Types == nil ||
		sc.ArchiveSize != key.size || sc.ArchiveModNano != key.modNano {
		return nil, false
	}
	types := make(map[string]struct{}, len(sc.Types))
	for _, typ := range sc.Types {
		types[typ] = struct{}{}
	}
	return types, true
}

// archiveTypes is the in-memory copy: each archive's type set, once a sidecar
// was read or a newest-first read went all the way through the archive.
//
// The key is the archive's path, size and modification time, so a file that
// is replaced under the same name is read again rather than trusted. Only a
// read that reached the end of the archive is remembered: a canceled read saw
// part of it, and a partial set would skip an archive that holds a match.
var archiveTypes = struct {
	sync.Mutex
	m map[archiveTypesKey]map[string]struct{}
}{m: map[archiveTypesKey]map[string]struct{}{}}

// archiveTypesMax bounds the memory: about one entry per archive retention
// keeps (43 on the town Mac). Past it the memory starts over, which costs one
// sidecar read each, never a wrong answer.
const archiveTypesMax = 1024

type archiveTypesKey struct {
	path    string
	size    int64
	modNano int64
}

// archiveTypesKeyOf keys a canonical .gz archive. A rotating-* file is not an
// archive yet (it can still be promoted under another name), so it is never
// keyed.
func archiveTypesKeyOf(src backfillSource) (archiveTypesKey, bool) {
	if src.kind != sourceArchive {
		return archiveTypesKey{}, false
	}
	return archiveTypesKeyAt(src.path)
}

// archiveTypesKeyAt keys the file at path as it is now.
func archiveTypesKeyAt(path string) (archiveTypesKey, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return archiveTypesKey{}, false
	}
	return archiveTypesKey{path: path, size: info.Size(), modNano: info.ModTime().UnixNano()}, true
}

// archiveTypesLookup returns the archive's type set from memory, else from
// its sidecar. ok is false when neither knows it: the caller must read the
// archive.
func archiveTypesLookup(key archiveTypesKey) (map[string]struct{}, bool) {
	archiveTypes.Lock()
	types, ok := archiveTypes.m[key]
	archiveTypes.Unlock()
	if ok {
		return types, true
	}
	types, ok = readTypesSidecar(key)
	if ok {
		archiveTypesRemember(key, types)
	}
	return types, ok
}

func archiveTypesRemember(key archiveTypesKey, types map[string]struct{}) {
	archiveTypes.Lock()
	defer archiveTypes.Unlock()
	if len(archiveTypes.m) >= archiveTypesMax {
		archiveTypes.m = map[archiveTypesKey]map[string]struct{}{}
	}
	archiveTypes.m[key] = types
}

// reapTypesSidecars removes, at startup, each sidecar whose archive is gone
// and each sidecar write that never reached its rename. Neither is ever read:
// a sidecar is only opened through its archive's path.
func reapTypesSidecars(dir string, names []string, stderr io.Writer) {
	for _, name := range names {
		path := filepath.Join(dir, name)
		if isTypesSidecarBasename(name) {
			if _, err := os.Stat(strings.TrimSuffix(path, typesSidecarSuffix)); !os.IsNotExist(err) {
				continue
			}
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "events: rotation: removing stale %q: %v\n", name, err) //nolint:errcheck // best-effort stderr
		}
	}
}
