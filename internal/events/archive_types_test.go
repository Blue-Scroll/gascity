package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// removeTypesSidecars deletes every archive type sidecar in dir, as if the
// archives were written before sidecars existed.
func removeTypesSidecars(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*"+typesSidecarSuffix))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			t.Fatal(err)
		}
	}
}

// sidecarTypes reads the sidecar beside archive, failing the test if there is
// no sidecar the read path would trust.
func sidecarTypes(t *testing.T, archive string) []string {
	t.Helper()
	key, ok := archiveTypesKeyAt(archive)
	if !ok {
		t.Fatalf("stat %s", archive)
	}
	types, ok := readTypesSidecar(key)
	if !ok {
		t.Fatalf("no trusted type sidecar beside %s", filepath.Base(archive))
	}
	return sortedTypes(types)
}

func sortedTypes(types map[string]struct{}) []string {
	out := make([]string, 0, len(types))
	for typ := range types {
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

func archivesIn(t *testing.T, dir string) []string {
	t.Helper()
	infos, err := archiveFilesIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, filepath.Join(dir, info.Basename))
	}
	return out
}

// TestRotationWritesTheArchiveTypeSet: gzipAndArchive leaves a sidecar beside
// each archive, and it holds exactly the types a full newest-first read of
// that archive finds. Equal to the read, not just to a list written here, so
// lineEventType cannot drift from scanSegmentNewest unnoticed.
func TestRotationWritesTheArchiveTypeSet(t *testing.T) {
	resetArchiveTypes(t)
	path, middle := seedTypedLog(t)
	if got, want := sidecarTypes(t, middle), []string{"decoy", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("middle archive types = %v, want %v", got, want)
	}
	for _, archive := range archivesIn(t, filepath.Dir(path)) {
		r, err := openSegmentReader(backfillSource{path: archive, kind: sourceArchive})
		if err != nil || r == nil {
			t.Fatalf("open %s: %v", archive, err)
		}
		floor := ^uint64(0)
		seg, err := scanSegmentNewest(context.Background(), r, Filter{}, &floor, 1)
		r.close()
		if err != nil || !seg.complete {
			t.Fatalf("scan %s: complete=%v err=%v", archive, seg.complete, err)
		}
		if got, want := sidecarTypes(t, archive), sortedTypes(seg.types); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: sidecar types %v, a full read finds %v", filepath.Base(archive), got, want)
		}
	}
}

// TestRotationRecordsATypeOnlyAFullDecodeFinds: a line in another field order
// is not read by lineHead, so its type reaches the sidecar only through the
// full decode. Left out, a cold typed read would skip the archive that holds
// the match.
func TestRotationRecordsATypeOnlyAFullDecodeFinds(t *testing.T) {
	resetArchiveTypes(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	src := filepath.Join(dir, "src")
	body := `{"seq":1,"type":"common","actor":"t"}` + "\n" +
		`{"type":"reordered","seq":2,"actor":"t"}` + "\n" +
		`{"seq":3,"type":"common","actor":"t"}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gzipInto(t, src, dir, 1, 3)
	writeJSONLEvents(t, path, 4, 5)
	archive := archivesIn(t, dir)[0]
	if got, want := sidecarTypes(t, archive), []string{"common", "reordered"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sidecar types = %v, want %v", got, want)
	}
	resetArchiveTypes(t)
	if got, _ := readNewest(t, path, Filter{Type: "reordered"}, 10); !reflect.DeepEqual(got, []uint64{2}) {
		t.Fatalf("cold type reordered = %v, want [2]", got)
	}
}

// TestReadNewestColdSkipsByTheSidecar is the point of the sidecar: with the
// in-memory sets empty, as after a gc start, a typed read still skips an
// archive that lacks its type. The archive is made unreadable, so opening it
// fails.
func TestReadNewestColdSkipsByTheSidecar(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs file modes that stop an open")
	}
	resetArchiveTypes(t)
	path, middle := seedTypedLog(t)
	all, err := ReadFiltered(path, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := seqsOf(ApplyFilter(all, Filter{Type: "want"}))
	resetArchiveTypes(t)
	if err := os.Chmod(middle, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(middle, 0o644) })

	if got, _ := readNewest(t, path, Filter{Type: "want"}, 1000); !reflect.DeepEqual(got, want) {
		t.Fatalf("cold read = %v, want %v", got, want)
	}
	// The fixture must prove something: without the sidecar the same cold
	// read opens the archive and fails.
	resetArchiveTypes(t)
	removeTypesSidecars(t, filepath.Dir(path))
	if _, _, err := ReadNewestWithInFlight(t.Context(), path, Filter{Type: "want"}, 1000); err == nil {
		t.Fatal("a cold read of an unreadable archive with no sidecar succeeded; the test proves nothing")
	}
}

// TestReadNewestDoesNotTrustABadSidecar: the middle archive holds one "late"
// event, and its sidecar is replaced by one that lacks "late". Only a sidecar
// written for exactly this file, in this version, may make the read skip; any
// other shape reads the archive and finds the event.
func TestReadNewestDoesNotTrustABadSidecar(t *testing.T) {
	type tamper func(t *testing.T, sidecar string, key archiveTypesKey)
	write := func(sc any) tamper {
		return func(t *testing.T, sidecar string, _ archiveTypesKey) {
			body, err := json.Marshal(sc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sidecar, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	lacking := func(key archiveTypesKey) typesSidecar {
		return typesSidecar{Version: typesSidecarVersion, ArchiveSize: key.size, ArchiveModNano: key.modNano, Types: []string{"decoy", "other"}}
	}
	cases := []struct {
		name     string
		tamper   tamper
		wantSkip bool
	}{
		// The control: a well-formed sidecar for this file is trusted, so the
		// fixture can tell a skip from a read.
		{"trusted", func(t *testing.T, sc string, key archiveTypesKey) { write(lacking(key))(t, sc, key) }, true},
		{"missing", func(t *testing.T, sc string, _ archiveTypesKey) {
			if err := os.Remove(sc); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"not json", func(t *testing.T, sc string, _ archiveTypesKey) {
			if err := os.WriteFile(sc, []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"another version", func(t *testing.T, sc string, key archiveTypesKey) {
			s := lacking(key)
			s.Version = typesSidecarVersion + 1
			write(s)(t, sc, key)
		}, false},
		{"another size", func(t *testing.T, sc string, key archiveTypesKey) {
			s := lacking(key)
			s.ArchiveSize++
			write(s)(t, sc, key)
		}, false},
		{"another mtime", func(t *testing.T, sc string, key archiveTypesKey) {
			s := lacking(key)
			s.ArchiveModNano++
			write(s)(t, sc, key)
		}, false},
		{"no types field", func(t *testing.T, sc string, key archiveTypesKey) {
			write(map[string]any{"v": typesSidecarVersion, "archive_size": key.size, "archive_mtime_ns": key.modNano})(t, sc, key)
		}, false},
		{"too large", func(t *testing.T, sc string, key archiveTypesKey) {
			body, err := json.Marshal(lacking(key))
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, bytes.Repeat([]byte(" "), typesSidecarMaxBytes)...)
			if err := os.WriteFile(sc, body, 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"unreadable", func(t *testing.T, sc string, key archiveTypesKey) {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("needs file modes that stop an open")
			}
			write(lacking(key))(t, sc, key)
			if err := os.Chmod(sc, 0); err != nil {
				t.Fatal(err)
			}
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetArchiveTypes(t)
			path, middle := seedTypedLog(t)
			_ = os.Remove(middle)
			archiveMixed(t, filepath.Dir(path), 201, 400, func(s uint64) string {
				if s == 302 {
					return "late"
				}
				return "other"
			})
			key, ok := archiveTypesKeyAt(middle)
			if !ok {
				t.Fatal("stat middle archive")
			}
			c.tamper(t, typesSidecarPath(middle), key)
			resetArchiveTypes(t)

			got, _ := readNewest(t, path, Filter{Type: "late"}, 10)
			if c.wantSkip {
				if len(got) != 0 {
					t.Fatalf("type late = %v; a trusted sidecar that lacks it must skip the archive", got)
				}
				return
			}
			if !reflect.DeepEqual(got, []uint64{302}) {
				t.Fatalf("type late = %v, want [302]", got)
			}
		})
	}
}

// TestReadNewestWritesTheSidecarAfterAFullRead: an archive written before
// sidecars existed gets one from the writer's first newest-first read that
// goes all the way through it, so the next gc process skips it cold. A
// read-only provider, and the plain read function, write nothing.
func TestReadNewestWritesTheSidecarAfterAFullRead(t *testing.T) {
	resetArchiveTypes(t)
	path, middle := seedTypedLog(t)
	dir := filepath.Dir(path)
	written := map[string][]string{}
	for _, archive := range archivesIn(t, dir) {
		written[archive] = sidecarTypes(t, archive)
	}
	absent := Filter{Type: "absent"} // reaches the end of every archive
	assertNoSidecarWritten := func(how string, list func() error) {
		t.Helper()
		removeTypesSidecars(t, dir)
		resetArchiveTypes(t)
		if err := list(); err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, "*"+typesSidecarSuffix+"*")); len(matches) != 0 {
			t.Fatalf("%s wrote %v; it must create no file", how, matches)
		}
	}
	assertNoSidecarWritten("ReadNewestWithInFlight", func() error {
		_, _, err := ReadNewestWithInFlight(t.Context(), path, absent, 10)
		return err
	})
	assertNoSidecarWritten("a read-only provider", func() error {
		_, _, err := NewReadOnlyFileProvider(path, &lockedBuffer{}).ListNewest(t.Context(), absent, 10)
		return err
	})

	removeTypesSidecars(t, dir)
	resetArchiveTypes(t)
	writer, err := NewFileRecorder(path, &lockedBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := writer.ListNewest(t.Context(), absent, 10); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for archive, want := range written {
		if got := sidecarTypes(t, archive); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the read wrote %v, the rotation wrote %v", filepath.Base(archive), got, want)
		}
	}

	// A new process: memory empty, the middle archive unreadable.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	resetArchiveTypes(t)
	if err := os.Chmod(middle, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(middle, 0o644) })
	if _, _, err := ReadNewestWithInFlight(t.Context(), path, Filter{Type: "want"}, 1000); err != nil {
		t.Fatalf("cold read after the sidecar was written: %v", err)
	}
}

// makeArchiveAt writes seqs first..last as an archive stamped ts, through the
// rotation's own gzipAndArchive, and returns its path.
func makeArchiveAt(t *testing.T, dir string, ts time.Time, first, last uint64) string {
	t.Helper()
	src := filepath.Join(dir, fmt.Sprintf("src-%d-%d", first, last))
	writeJSONLEvents(t, src, seqRange(first, last)...)
	dest := filepath.Join(dir, formatArchiveBasename(ts, first, last))
	var stderr bytes.Buffer
	if err := gzipAndArchive(src, dest, &stderr); err != nil {
		t.Fatalf("gzipAndArchive: %v (%s)", err, stderr.String())
	}
	if _, err := os.Stat(typesSidecarPath(dest)); err != nil {
		t.Fatalf("no sidecar beside %s: %v", filepath.Base(dest), err)
	}
	return dest
}

// TestRetentionRemovesTheSidecarWithItsArchive: an expired archive's sidecar
// goes with it, and a kept archive keeps its own.
func TestRetentionRemovesTheSidecarWithItsArchive(t *testing.T) {
	dir := t.TempDir()
	old := makeArchiveAt(t, dir, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), 1, 5)
	kept := makeArchiveAt(t, dir, time.Now().UTC(), 6, 10)
	var stderr bytes.Buffer
	if err := reapExpiredArchives(dir, 24*time.Hour, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{old, typesSidecarPath(old)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived retention: %v", filepath.Base(gone), err)
		}
	}
	for _, stays := range []string{kept, typesSidecarPath(kept)} {
		if _, err := os.Stat(stays); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(stays), err)
		}
	}
}

// TestStartupReaperRemovesStrandedSidecars: at open, a sidecar whose archive
// is gone and a sidecar write that never reached its rename are removed; a
// sidecar with its archive stays.
func TestStartupReaperRemovesStrandedSidecars(t *testing.T) {
	dir := t.TempDir()
	kept := makeArchiveAt(t, dir, time.Now().UTC(), 1, 5)
	gone := makeArchiveAt(t, dir, time.Now().UTC(), 6, 10)
	if err := os.Remove(gone); err != nil { // as an older binary's retention would
		t.Fatal(err)
	}
	tmp := typesSidecarPath(kept) + ".123456.tmp"
	if err := os.WriteFile(tmp, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := reapOrphanedRotatingFiles(dir, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{typesSidecarPath(gone), tmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the startup reaper: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(typesSidecarPath(kept)); err != nil {
		t.Errorf("the sidecar of a live archive was removed: %v", err)
	}
}

// TestTypesSidecarNamesAreInvisibleToArchiveScanners: no scanner of the
// events directory takes a sidecar, or a sidecar write in flight, for an
// archive, a legacy archive, a rotating file or a stale .gz.tmp.
func TestTypesSidecarNamesAreInvisibleToArchiveScanners(t *testing.T) {
	archive := formatArchiveBasename(time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC), 1, 5)
	sidecar := archive + typesSidecarSuffix
	tmp := sidecar + ".123456.tmp"
	for _, name := range []string{sidecar, tmp} {
		if isCanonicalArchiveBasename(name) || isLegacyArchiveBasename(name) || hasRotatingPrefix(name) || hasGzipTmpSuffix(name) {
			t.Errorf("%s matches an archive scanner", name)
		}
		if _, err := parseArchiveBasename(name); err == nil {
			t.Errorf("%s parses as an archive", name)
		}
	}
	if !isTypesSidecarBasename(sidecar) || isTypesSidecarBasename(tmp) || isTypesSidecarBasename(archive) {
		t.Error("isTypesSidecarBasename names the wrong files")
	}
	if !isTypesSidecarTmpBasename(tmp) || isTypesSidecarTmpBasename(sidecar) || isTypesSidecarTmpBasename(archive+".tmp") {
		t.Error("isTypesSidecarTmpBasename names the wrong files")
	}

	resetArchiveTypes(t)
	path, _ := seedTypedLog(t)
	dir := filepath.Dir(path)
	srcs, err := listBackfillSources(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range srcs {
		if strings.HasSuffix(src.path, typesSidecarSuffix) {
			t.Errorf("listBackfillSources lists the sidecar %s", filepath.Base(src.path))
		}
	}
	if n := len(archivesIn(t, dir)); n != 3 || len(srcs) != 3 {
		t.Errorf("archiveFilesIn = %d, listBackfillSources = %d, want 3 each", n, len(srcs))
	}
}
