package sessionlog

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// windowLog builds a Claude JSONL log whose lines are padded so the newest
// entries sit well inside the first window and the whole file is larger than
// it. Each add links the new entry to the one before it.
type windowLog struct {
	lines []string
	prev  string
	n     int
	start time.Time
}

func newWindowLog() *windowLog {
	return &windowLog{start: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
}

// pad keeps each line about 2 KB, so 400 entries make an 800 KB file.
var windowPad = strings.Repeat("x", 2000)

func (l *windowLog) add(typ string, fields map[string]any) string {
	l.n++
	id := fmt.Sprintf("e%04d", l.n)
	entry := map[string]any{
		"type":       typ,
		"uuid":       id,
		"parentUuid": l.prev,
		"timestamp":  l.start.Add(time.Duration(l.n) * time.Second).Format(time.RFC3339Nano),
	}
	for k, v := range fields {
		entry[k] = v
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		panic(err)
	}
	l.lines = append(l.lines, string(raw))
	l.prev = id
	return id
}

func (l *windowLog) text(role string) string {
	return l.add(role, map[string]any{"message": map[string]any{"role": role, "content": windowPad}})
}

// turn adds a user prompt, a tool call and its result.
func (l *windowLog) turn() {
	l.text("user")
	call := "call-" + fmt.Sprint(l.n+1)
	l.add("assistant", map[string]any{"message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "id": call, "name": "Bash", "input": map[string]any{"command": windowPad}},
	}}})
	l.add("user", map[string]any{"message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": call, "content": "ok"},
	}}})
}

func (l *windowLog) compactBoundary(logicalParent string) string {
	l.prev = ""
	return l.add("system", map[string]any{"subtype": "compact_boundary", "logicalParentUuid": logicalParent, "content": "Conversation compacted"})
}

func (l *windowLog) write(t *testing.T) string {
	t.Helper()
	return writeJSONL(t, l.lines...)
}

// pageOf cuts a page the way the worker adapter does, for a whole-file and a
// windowed session alike.
func pageOf(t *testing.T, sess *Session, before string, limit int) *Session {
	t.Helper()
	page, err := PageSession(sess, 0, before, "")
	if err != nil {
		t.Fatalf("PageSession: %v", err)
	}
	LimitSession(page, limit, false)
	return page
}

func readWhole(t *testing.T, path string, raw bool) *Session {
	t.Helper()
	read := ReadProviderFile
	if raw {
		read = ReadProviderFileRaw
	}
	sess, err := read("claude", path, 0)
	if err != nil {
		t.Fatalf("whole-file read: %v", err)
	}
	return sess
}

func readWindow(t *testing.T, path string, limit int, before string, raw bool) *Session {
	t.Helper()
	sess, ok, err := ReadProviderFileWindow("claude", path, limit, before, raw)
	if err != nil {
		t.Fatalf("ReadProviderFileWindow: %v", err)
	}
	if !ok {
		t.Fatalf("ReadProviderFileWindow could not answer; want a window answer")
	}
	return sess
}

func assertSamePage(t *testing.T, whole, window *Session) {
	t.Helper()
	if got, want := limitTestIDs(window.Messages), limitTestIDs(whole.Messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("window page = %v\nwhole page  = %v", got, want)
	}
	w, f := *window.Pagination, *whole.Pagination
	if !w.PartialRead {
		t.Fatalf("window pagination = %+v, want partial_read", w)
	}
	if f.PartialRead {
		t.Fatalf("whole-file pagination = %+v, want no partial_read", f)
	}
	if w.HasOlderMessages != f.HasOlderMessages || w.HasNewerMessages != f.HasNewerMessages ||
		w.TruncatedBeforeMessage != f.TruncatedBeforeMessage || w.ReturnedMessageCount != f.ReturnedMessageCount {
		t.Fatalf("window pagination = %+v\nwhole pagination  = %+v", w, f)
	}
	if w.TotalMessageCount > f.TotalMessageCount || w.TotalMessageCount <= w.ReturnedMessageCount {
		t.Fatalf("window total_message_count = %d, want more than the page and at most the whole %d", w.TotalMessageCount, f.TotalMessageCount)
	}
}

// The newest page from the end of the file is the newest page of the whole
// file, in both views, and so is the page before it. 59 puts the page edge
// between a tool call and its result, so the window must reach above the page
// for the call.
func TestReadProviderFileWindowReturnsTheWholeFilePage(t *testing.T) {
	log := newWindowLog()
	for i := 0; i < 134; i++ {
		log.turn()
	}
	path := log.write(t)

	for _, raw := range []bool{true, false} {
		t.Run(fmt.Sprintf("raw=%v", raw), func(t *testing.T) {
			whole := readWhole(t, path, raw)
			window := readWindow(t, path, 59, "", raw)
			if len(window.Messages) >= len(whole.Messages) {
				t.Fatalf("window read %d of %d entries; it should not need the whole file", len(window.Messages), len(whole.Messages))
			}
			newest := pageOf(t, whole, "", 59)
			assertSamePage(t, newest, pageOf(t, window, "", 59))

			before := newest.Pagination.TruncatedBeforeMessage
			older := readWindow(t, path, 59, before, raw)
			assertSamePage(t, pageOf(t, whole, before, 59), pageOf(t, older, before, 59))
		})
	}
}

// A compaction inside the window: the page walks across the boundary through
// its logicalParentUuid, the same way the whole-file read does.
func TestReadProviderFileWindowFollowsACompactionInsideTheWindow(t *testing.T) {
	log := newWindowLog()
	for i := 0; i < 120; i++ {
		log.turn()
	}
	boundary := log.compactBoundary(log.prev)
	for i := 0; i < 10; i++ {
		log.turn()
	}
	path := log.write(t)

	whole := readWhole(t, path, true)
	window := readWindow(t, path, 60, "", true)
	page := pageOf(t, window, "", 60)
	assertSamePage(t, pageOf(t, whole, "", 60), page)
	if !strings.Contains(strings.Join(limitTestIDs(page.Messages), ","), boundary) {
		t.Fatalf("page %v does not cross the boundary %s", limitTestIDs(page.Messages), boundary)
	}
}

// A compaction whose logicalParentUuid is in no line at all: the whole-file
// read continues from the line above the boundary, and the end of the file
// cannot tell where that is. The window read must leave it to the whole read.
func TestReadProviderFileWindowFallsBackAtABoundaryWithAMissingParent(t *testing.T) {
	log := newWindowLog()
	for i := 0; i < 120; i++ {
		log.turn()
	}
	log.compactBoundary("not-in-this-file")
	for i := 0; i < 10; i++ {
		log.turn()
	}
	path := log.write(t)

	if _, ok, err := ReadProviderFileWindow("claude", path, 60, "", true); err != nil || ok {
		t.Fatalf("ReadProviderFileWindow = ok %v, err %v; want no answer, so the caller reads the whole file", ok, err)
	}
}

func TestReadProviderFileWindowFallsBack(t *testing.T) {
	big := newWindowLog()
	for i := 0; i < 134; i++ {
		big.turn()
	}

	small := newWindowLog()
	for i := 0; i < 10; i++ {
		small.turn()
	}

	// Two tips share the newest timestamp. The whole-file read breaks that tie
	// on the length of each whole branch, which the window cannot see.
	tied := newWindowLog()
	for i := 0; i < 134; i++ {
		tied.turn()
	}
	last := tied.lines[len(tied.lines)-1]
	var lastEntry map[string]any
	if err := json.Unmarshal([]byte(last), &lastEntry); err != nil {
		t.Fatal(err)
	}
	lastEntry["uuid"] = "twin"
	twin, _ := json.Marshal(lastEntry)
	tied.lines = append(tied.lines, string(twin))

	// The page's last result answers a call made at the top of the file, above
	// every window that is smaller than the file.
	lateResult := newWindowLog()
	lateResult.add("assistant", map[string]any{"message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "id": "slow-call", "name": "Task", "input": map[string]any{}},
	}}})
	for i := 0; i < 134; i++ {
		lateResult.turn()
	}
	lateResult.add("user", map[string]any{"message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "slow-call", "content": "done"},
	}}})

	cases := []struct {
		name     string
		provider string
		log      *windowLog
		limit    int
		before   string
	}{
		{name: "not claude jsonl", provider: "codex", log: big, limit: 60},
		{name: "no limit", provider: "claude", log: big, limit: 0},
		{name: "file smaller than the first window", provider: "claude", log: small, limit: 5},
		{name: "two newest tips", provider: "claude", log: tied, limit: 60},
		{name: "cursor on no branch", provider: "claude", log: big, limit: 60, before: "no-such-entry"},
		{name: "result whose call is above every window", provider: "claude", log: lateResult, limit: 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.log.write(t)
			sess, ok, err := ReadProviderFileWindow(tc.provider, path, tc.limit, tc.before, true)
			if err != nil || ok || sess != nil {
				t.Fatalf("ReadProviderFileWindow = %v, ok %v, err %v; want no answer and no error", sess, ok, err)
			}
		})
	}
}

// A family missing from one map would be read as Claude JSONL in that view
// and with its own reader in the other.
func TestFamilyReadersNameTheSameFamilies(t *testing.T) {
	keys := func(m map[string]func(string, int) (*Session, error)) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if got, want := keys(rawFamilyReaders), keys(familyReaders); !reflect.DeepEqual(got, want) {
		t.Fatalf("rawFamilyReaders = %v\nfamilyReaders    = %v", got, want)
	}
	for _, family := range keys(familyReaders) {
		if ProviderFamily(family) != family {
			t.Errorf("ProviderFamily(%q) = %q; the map key must be the family name", family, ProviderFamily(family))
		}
		if readsClaudeJSONL(family) {
			t.Errorf("readsClaudeJSONL(%q) = true; it has its own reader", family)
		}
	}
	for _, provider := range []string{"claude", "claude-opus-high", ""} {
		if !readsClaudeJSONL(provider) {
			t.Errorf("readsClaudeJSONL(%q) = false, want true", provider)
		}
	}
}
