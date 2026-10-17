package api

import (
	"context"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/worker"
)

// structuredStreamReloadSlack is how many entries past its resume token's
// window a reload asks the end of the log for. A reload runs on every write to
// the log and every 2 s poll, so this covers far more than one burst of
// writes. A burst bigger than this falls back to the whole read once. Kept
// small on purpose: the mayor's entries average about 5 KB, so 132 entries is
// one 1 MB window read. Measured 2026-10-17 at load 100 on the mayor's 23.8 MB
// log: a whole read took 23 to 32 s, a read of the last 232 entries 1.2 s.
const structuredStreamReloadSlack = 100

// structuredStreamReload is the history read a live structured stream makes
// each time its transcript may have changed: a write to the log, the 2 s poll,
// a worker operation. It used to be a whole-log read every time. For the
// mayor's log (23 MB on 2026-10-17) that parsed and normalized every entry on
// every write and every poll, for every open view, and it became the biggest
// CPU load on the box (vn-u1p1nw1). Now a reload:
//
//  1. Reads nothing when the log is the same file at the same generation
//     (mtime and size) as the last read: that read would come back the same.
//  2. Reads only the end of the log when the stream's resume token is a
//     suffix window and the end still holds that whole window. The stream
//     keeps its token that small (boundedStructuredResumeToken).
//  3. Reads the whole log otherwise, exactly as before.
//
// A read of the end of the log is a partial read: its history envelope's
// transcript-wide facts (compaction count, branch flag, diagnostics) cover
// only that end, and its pagination says partial_read, the same contract a
// limited transcript GET already has (vn-t7v7fm4).
type structuredStreamReload struct {
	handle          worker.HistoryHandle
	includeThinking bool
	// seen is the log the last successful read observed, as
	// filepath.Clean(path) and its generation ID. Empty means read.
	seenPath       string
	seenGeneration string
}

// newStructuredStreamReload starts a reload loop for a stream that has
// already read initial, so the first reload of an unchanged log reads nothing.
func newStructuredStreamReload(handle worker.HistoryHandle, includeThinking bool, initial *worker.HistorySnapshot) *structuredStreamReload {
	r := &structuredStreamReload{handle: handle, includeThinking: includeThinking}
	if initial != nil && initial.TranscriptStreamID != "" && initial.Generation.ID != "" {
		r.seenPath = filepath.Clean(initial.TranscriptStreamID)
		r.seenGeneration = initial.Generation.ID
	}
	return r
}

// read returns the history snapshot to project against resumeToken, the
// stream's current token. A nil snapshot with a nil error means the log has
// not changed since the last read, so there is nothing new to project.
func (r *structuredStreamReload) read(ctx context.Context, resumeToken string) (*worker.HistorySnapshot, error) {
	ctx = worker.WithoutOperationEvents(ctx)
	// Observe the log before reading it. A write that lands during the read
	// then shows up as a new generation next time, and is read then.
	path := sessionStreamTranscriptPath(ctx, r.handle)
	var generation string
	if path != "" {
		path = filepath.Clean(path)
		if id, err := worker.TranscriptGenerationID(path); err == nil {
			generation = id
		}
	}
	if generation != "" && path == r.seenPath && generation == r.seenGeneration {
		return nil, nil
	}

	snapshot, err := r.readWindow(ctx, resumeToken)
	if err == nil && snapshot == nil {
		snapshot, err = r.handle.History(ctx, worker.HistoryRequest{})
	}
	if err != nil {
		return nil, err
	}
	r.seenPath, r.seenGeneration = path, generation
	return snapshot, nil
}

// readWindow reads the end of the log and returns it only when it holds the
// whole window resumeToken names, on the same stream. Otherwise it returns nil
// and the caller reads the whole log, which also decides any reset.
func (r *structuredStreamReload) readWindow(ctx context.Context, resumeToken string) (*worker.HistorySnapshot, error) {
	token, ok := decodeStructuredResumeToken(resumeToken)
	if !ok || !token.SuffixWindow || token.IncludeThinking != r.includeThinking {
		return nil, nil
	}
	snapshot, err := r.handle.History(ctx, worker.HistoryRequest{Limit: token.MessageCount + structuredStreamReloadSlack})
	if err != nil || snapshot == nil {
		return nil, err
	}
	if structuredStreamIdentityHash(structuredHistoryFromSnapshot(snapshot)) != token.StreamSHA256 {
		return nil, nil
	}
	messages, _ := historySnapshotStructuredMessages(snapshot, r.includeThinking)
	if _, _, reason := structuredSuffixWindowRange(token, messages); reason != "" {
		return nil, nil
	}
	return snapshot, nil
}
