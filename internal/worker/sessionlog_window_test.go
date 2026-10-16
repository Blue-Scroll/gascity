package worker

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeLongClaudeLog writes a Claude JSONL log of turns (a prompt, a Read call
// and its result), padded so it is several times larger than the first window
// the limited read tries.
func writeLongClaudeLog(t *testing.T, turns int) string {
	t.Helper()
	pad := strings.Repeat("x", 2000)
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var lines []string
	prev := ""
	add := func(typ string, message any) {
		id := fmt.Sprintf("e%04d", len(lines)+1)
		raw, err := json.Marshal(map[string]any{
			"type":       typ,
			"uuid":       id,
			"parentUuid": prev,
			"timestamp":  start.Add(time.Duration(len(lines)) * time.Second).Format(time.RFC3339Nano),
			"message":    message,
		})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(raw))
		prev = id
	}
	for i := 0; i < turns; i++ {
		call := fmt.Sprintf("call-%d", i)
		add("user", map[string]any{"role": "user", "content": pad})
		add("assistant", map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": call, "name": "Read", "input": map[string]any{"file_path": "/tmp/" + call + ".go"}},
		}})
		add("user", map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": call, "content": "line1\nline2\n"},
		}})
	}
	path := filepath.Join(t.TempDir(), "long.jsonl")
	writeLines(t, path, lines...)
	return path
}

func historyEntryWithoutOrder(t *testing.T, entry HistoryEntry) string {
	t.Helper()
	entry.Order = 0
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A limited history read of a long Claude log comes from the end of the file
// (vn-t7v7fm4) and gives the same page as the whole log: the same entries, a
// result typed from its call above the page, and the same cursor. Only
// partial_read tells them apart. 450 entries less 58 starts the page on a tool
// result.
func TestLoadHistoryLimitReadsTheEndOfALongLog(t *testing.T) {
	t.Parallel()
	path := writeLongClaudeLog(t, 150)
	adapter := SessionLogAdapter{}

	whole, err := adapter.LoadHistory(LoadRequest{Provider: "claude", TranscriptPath: path})
	if err != nil {
		t.Fatalf("whole LoadHistory: %v", err)
	}
	limited, err := adapter.LoadHistory(LoadRequest{Provider: "claude", TranscriptPath: path, Limit: 58})
	if err != nil {
		t.Fatalf("limited LoadHistory: %v", err)
	}

	if p := limited.Pagination; p == nil || !p.PartialRead || !p.HasOlderMessages || p.ReturnedMessageCount != 58 {
		t.Fatalf("limited pagination = %+v, want partial_read, older messages and 58 returned", p)
	}
	if p := limited.Pagination; p.TotalMessageCount >= len(whole.Entries) {
		t.Fatalf("total_message_count = %d of %d entries; the limited read should not have read the whole log", p.TotalMessageCount, len(whole.Entries))
	}
	newest := whole.Entries[len(whole.Entries)-58:]
	for i := range limited.Entries {
		if got, want := historyEntryWithoutOrder(t, limited.Entries[i]), historyEntryWithoutOrder(t, newest[i]); got != want {
			t.Fatalf("entry %d:\n limited = %.400s\n whole   = %.400s", i, got, want)
		}
	}
	first := limited.Entries[0].Blocks
	if len(first) != 1 || first[0].Kind != BlockKindToolResult || first[0].StructuredResult == nil || first[0].StructuredResult.Kind != "read" {
		t.Fatalf("first entry blocks = %+v, want a read result typed from its call above the page", first)
	}
	if limited.Cursor != whole.Cursor || limited.TailState.LastEntryID != whole.TailState.LastEntryID {
		t.Fatalf("limited cursor %+v / last %q, whole %+v / last %q", limited.Cursor, limited.TailState.LastEntryID, whole.Cursor, whole.TailState.LastEntryID)
	}
}

// The conversation and raw transcript formats take the same limited read.
func TestReadTranscriptLimitReadsTheEndOfALongLog(t *testing.T) {
	t.Parallel()
	path := writeLongClaudeLog(t, 150)
	adapter := SessionLogAdapter{}

	for _, raw := range []bool{false, true} {
		whole, err := adapter.ReadTranscript(TranscriptRequest{Provider: "claude", TranscriptPath: path, Raw: raw})
		if err != nil {
			t.Fatalf("raw=%v whole ReadTranscript: %v", raw, err)
		}
		limited, err := adapter.ReadTranscript(TranscriptRequest{Provider: "claude", TranscriptPath: path, Raw: raw, Limit: 60})
		if err != nil {
			t.Fatalf("raw=%v limited ReadTranscript: %v", raw, err)
		}
		if p := limited.Session.Pagination; p == nil || !p.PartialRead || !p.HasOlderMessages {
			t.Fatalf("raw=%v limited pagination = %+v, want partial_read and older messages", raw, p)
		}
		ids := func(result *TranscriptResult) []string {
			var out []string
			for _, m := range result.Session.Messages {
				out = append(out, m.UUID)
			}
			return out
		}
		want := ids(whole)
		want = want[len(want)-60:]
		if got := ids(limited); !reflect.DeepEqual(got, want) {
			t.Fatalf("raw=%v limited = %v\nwant %v", raw, got, want)
		}
		if raw && len(limited.RawMessages) != 60 {
			t.Fatalf("raw messages = %d, want 60", len(limited.RawMessages))
		}
	}
}
