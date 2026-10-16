package sessionlog

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The window read starts with the last windowFirstBytes of the file and
// multiplies the window by windowGrowth until it can answer or holds the whole
// file. Each round re-reads from the end, so the bytes parsed stay under 4/3
// of the last window.
const (
	windowFirstBytes = 256 * 1024
	windowGrowth     = 4
)

// ReadProviderFileWindow answers a read for the newest limit entries (or the
// limit entries before beforeEntryID) from the end of a Claude JSONL file,
// without parsing the rest of it. A whole-file read parses every line and
// builds the whole message tree to keep 60 entries: for the mayor's 6 MB log
// that cost 4 to 30 s under load (vn-t7v7fm4).
//
// The returned session's Messages are a suffix of the active branch: the page
// plus the entries above it that were read. It is not cut to the page. Page it
// with PageSession and LimitSession, exactly as a whole-file session, and the
// page comes out the same. Partial is set, so the page's PaginationInfo says
// PartialRead, and the transcript-wide fields cover only what was read.
//
// The suffix it returns holds more than limit entries before the page end
// unless it reaches the branch root, so LimitSession always reports the older
// entries the window left out. It also holds the tool_use of every tool_result
// on the page, so a reader that pairs results with their calls sees the same
// pairs as a whole-file read.
//
// ok is false, with no error, whenever the end of the file cannot answer on
// its own. The caller then reads the whole file. That is the case for a
// provider whose transcript is not Claude JSONL, limit <= 0, a window that
// grew to the whole file, two branch tips with the same newest timestamp (the
// whole-file read breaks that tie on the length of each whole branch), and a
// cursor the window never reached.
//
// One assumption it cannot check without reading the whole file: no line
// above the window carries a later timestamp than the newest branch tip in
// the window. Claude Code stamps each line when it writes it, so the newest
// tip is near the end of the file.
func ReadProviderFileWindow(provider, path string, limit int, beforeEntryID string, raw bool) (sess *Session, ok bool, err error) {
	if limit <= 0 || !readsClaudeJSONL(provider) {
		return nil, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file
	info, err := f.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat session file: %w", err)
	}
	size := info.Size()

	for window := int64(windowFirstBytes); window < size; window *= windowGrowth {
		data := make([]byte, window)
		if _, err := f.ReadAt(data, size-window); err != nil && err != io.EOF {
			return nil, false, fmt.Errorf("reading session file: %w", err)
		}
		// The window starts mid-line. Drop that partial line, or the parser
		// counts it as malformed.
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			continue // one line longer than the window; grow
		}
		sess, ok, err := answerFromWindow(data[newline+1:], limit, strings.TrimSpace(beforeEntryID), raw)
		if err != nil || ok {
			if sess != nil {
				sess.ID = sessionIDFromPath(path)
			}
			return sess, ok, err
		}
	}
	return nil, false, nil
}

// answerFromWindow resolves the entries of one window, or reports that the
// window cannot answer yet.
func answerFromWindow(data []byte, limit int, beforeEntryID string, raw bool) (*Session, bool, error) {
	entries, diagnostics, err := parseEntries(bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	index := indexDag(entries)
	tip, tied, found := index.activeTip()
	if !found || tied {
		return nil, false, nil
	}
	branch, _, cut := index.walkToRoot(tip, false)

	messages := branch
	if !raw {
		messages = nil
		for _, e := range branch {
			if displayTypes[e.Type] {
				messages = append(messages, e)
			}
		}
	}

	// end is where the page ends: the tip, or the before cursor (-1 when the
	// cursor is not on the branch read so far).
	end := len(messages)
	if beforeEntryID != "" {
		end = -1
		for i, m := range messages {
			if m.UUID == beforeEntryID {
				end = i
				break
			}
		}
	}
	// A branch that runs above the window must hold the cursor and at least
	// one entry above the page, so LimitSession trims and reports that older
	// entries exist. A branch that reaches its root is the whole branch: a
	// cursor missing from it is missing from the file, and PageSession says so.
	if cut && end <= limit {
		return nil, false, nil
	}
	if cut && !toolUsesPrecede(messages[end-limit:end], branch) {
		return nil, false, nil
	}

	result := index.result(entries, branch, 0)
	sess := &Session{
		Messages:           messages,
		OrphanedToolUseIDs: result.OrphanedToolUseIDs,
		HasBranches:        result.HasBranches,
		Diagnostics:        diagnostics,
		Partial:            true,
	}
	if err := validateUniqueEntryIDs(sess); err != nil {
		return nil, false, err
	}
	return sess, true, nil
}

// toolUsesPrecede reports whether every tool_result on page names a tool_use
// that is in branch. A whole-file read pairs a page's results against the whole
// branch; a result whose call sits above the window would lose its pairing.
func toolUsesPrecede(page, branch []*Entry) bool {
	uses := make(map[string]bool)
	for _, e := range branch {
		for _, b := range e.ContentBlocks() {
			if b.Type == "tool_use" && b.ID != "" {
				uses[b.ID] = true
			}
		}
	}
	for _, e := range page {
		for _, b := range e.ContentBlocks() {
			if b.Type == "tool_result" && b.ToolUseID != "" && !uses[b.ToolUseID] {
				return false
			}
		}
	}
	return true
}

// sessionIDFromPath is a Claude session's ID: its file name without the
// extension.
func sessionIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
