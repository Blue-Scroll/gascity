package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

const (
	sessionStructuredOperationSnapshot = "snapshot"
	sessionStructuredOperationUpsert   = "upsert"
	sessionStructuredOperationReset    = "reset"

	sessionStructuredResetResumeInvalid     = "resume_invalid"
	sessionStructuredResetStreamChanged     = "stream_changed"
	sessionStructuredResetCursorInvalidated = "cursor_invalidated"
	sessionStructuredResetHistoryRewritten  = "history_rewritten"

	sessionStructuredResumeTokenPrefix = "st1."
	sessionStructuredResumeTokenMaxLen = 2048
)

type sessionStructuredResumeTokenV1 struct {
	Version          int    `json:"v"`
	StreamSHA256     string `json:"stream_sha256"`
	AfterEntryID     string `json:"after_entry_id,omitempty"`
	MessageCount     int    `json:"message_count"`
	PrefixSHA256     string `json:"prefix_sha256"`
	ProjectionSHA256 string `json:"projection_sha256"`
	IncludeThinking  bool   `json:"include_thinking"`
	SuffixWindow     bool   `json:"suffix_window,omitempty"`
}

// buildStructuredStreamUpdate compares an opaque client resume token with the
// current authoritative projection. A nil result means the client already has
// this exact projection. Upserts replay the previous mutable tail inclusively,
// so a partial message can become final without changing its stable ID.
func buildStructuredStreamUpdate(resumeToken string, projection SessionStreamStructuredMessageEvent, includeThinking bool) *SessionStreamStructuredMessageEvent {
	currentToken := structuredResumeToken(projection, includeThinking)
	currentEncoded := encodeStructuredResumeToken(currentToken)
	current := cloneStructuredStreamProjection(projection, currentEncoded)

	if strings.TrimSpace(resumeToken) == "" {
		current.Operation = sessionStructuredOperationSnapshot
		return &current
	}

	previous, ok := decodeStructuredResumeToken(resumeToken)
	if !ok || previous.IncludeThinking != includeThinking {
		return structuredResetUpdate(current, sessionStructuredResetResumeInvalid)
	}
	if previous.StreamSHA256 != currentToken.StreamSHA256 {
		return structuredResetUpdate(current, sessionStructuredResetStreamChanged)
	}
	if previous.SuffixWindow {
		windowStart, cursorIndex, resetReason := structuredSuffixWindowRange(previous, current.StructuredMessages)
		if resetReason != "" {
			return structuredResetUpdate(current, resetReason)
		}
		if previous.MessageCount > 0 && cursorIndex == len(current.StructuredMessages)-1 {
			window := projection
			window.StructuredMessages = append([]SessionStructuredMessage(nil), projection.StructuredMessages[windowStart:cursorIndex+1]...)
			window.Pagination = nil
			if previous.ProjectionSHA256 == hashStructuredProjection(window, includeThinking) {
				return nil
			}
		}

		current.Operation = sessionStructuredOperationUpsert
		current.StructuredMessages = append([]SessionStructuredMessage(nil), current.StructuredMessages[cursorIndex:]...)
		return &current
	}
	if previous.ProjectionSHA256 == currentToken.ProjectionSHA256 {
		return nil
	}
	if previous.MessageCount > len(current.StructuredMessages) {
		return structuredResetUpdate(current, sessionStructuredResetCursorInvalidated)
	}
	if previous.MessageCount > 0 {
		cursorIndex := previous.MessageCount - 1
		if current.StructuredMessages[cursorIndex].ID != previous.AfterEntryID {
			return structuredResetUpdate(current, sessionStructuredResetCursorInvalidated)
		}
		if hashStructuredMessages(current.StructuredMessages[:cursorIndex]) != previous.PrefixSHA256 {
			return structuredResetUpdate(current, sessionStructuredResetHistoryRewritten)
		}
	}

	start := 0
	if previous.MessageCount > 0 {
		start = previous.MessageCount - 1
	}
	current.Operation = sessionStructuredOperationUpsert
	current.StructuredMessages = append([]SessionStructuredMessage(nil), current.StructuredMessages[start:]...)
	if current.StructuredMessages == nil {
		current.StructuredMessages = []SessionStructuredMessage{}
	}
	return &current
}

// liveStructuredStreamUpdate is buildStructuredStreamUpdate for a live
// stream's history reads. It also returns the token the stream compares its
// next read against, which is bounded (boundedStructuredResumeToken) whenever
// projection has a message. A frame it returns carries that same token, so a
// client that reconnects resumes from what the stream holds.
func liveStructuredStreamUpdate(resumeToken string, projection SessionStreamStructuredMessageEvent, includeThinking bool) (*SessionStreamStructuredMessageEvent, string) {
	update := buildStructuredStreamUpdate(resumeToken, projection, includeThinking)
	next := resumeToken
	if update != nil {
		next = update.History.Cursor.ResumeToken
	}
	// A nil update means the client already holds projection, so a token for
	// projection's newest messages describes the client as well as one for
	// all of them does.
	if bounded, ok := boundedStructuredResumeToken(projection, includeThinking); ok {
		next = bounded
		if update != nil {
			update.History.Cursor.ResumeToken = bounded
		}
	}
	return update, next
}

// structuredStreamTokenWindow is how many of the newest messages a live
// structured stream's resume token names. See boundedStructuredResumeToken.
const structuredStreamTokenWindow = 32

// boundedStructuredResumeToken is a resume token for a client that holds
// projection, naming only its newest structuredStreamTokenWindow messages as a
// suffix window. A live stream keeps its token this small so that each reload
// can check it against the end of the log alone (structuredStreamReload)
// instead of re-reading and re-hashing every message the stream ever sent
// (vn-u1p1nw1). A suffix token resumes with upserts on any read that holds its
// window, the whole log included (vn-c5p1mf0). What it gives up: a rewrite
// above the window no longer resets the client. ok is false when projection
// has no messages, because a suffix token needs a message to anchor on.
func boundedStructuredResumeToken(projection SessionStreamStructuredMessageEvent, includeThinking bool) (string, bool) {
	messages := projection.StructuredMessages
	if len(messages) == 0 {
		return "", false
	}
	if len(messages) > structuredStreamTokenWindow {
		messages = messages[len(messages)-structuredStreamTokenWindow:]
	}
	window := projection
	window.StructuredMessages = messages
	// Without pagination the token hashes window exactly as the suffix branch
	// of buildStructuredStreamUpdate hashes the same messages on the next read.
	window.Pagination = nil
	token := structuredResumeToken(window, includeThinking)
	token.SuffixWindow = true
	return encodeStructuredResumeToken(token), true
}

func structuredSuffixWindowRange(token sessionStructuredResumeTokenV1, messages []SessionStructuredMessage) (int, int, string) {
	cursorIndex := -1
	for i := range messages {
		if messages[i].ID == token.AfterEntryID {
			cursorIndex = i
			break
		}
	}
	if cursorIndex < 0 {
		return 0, 0, sessionStructuredResetCursorInvalidated
	}
	windowStart := cursorIndex + 1
	prefixEnd := windowStart
	if token.MessageCount > 0 {
		windowStart = cursorIndex - token.MessageCount + 1
		prefixEnd = cursorIndex
	}
	if windowStart < 0 {
		return 0, 0, sessionStructuredResetCursorInvalidated
	}
	if hashStructuredMessages(messages[windowStart:prefixEnd]) != token.PrefixSHA256 {
		return 0, 0, sessionStructuredResetHistoryRewritten
	}
	return windowStart, cursorIndex, ""
}

func structuredSnapshotProjection(projection SessionStreamStructuredMessageEvent, includeThinking bool) SessionStreamStructuredMessageEvent {
	return *buildStructuredStreamUpdate("", projection, includeThinking)
}

func structuredTranscriptResponseFromEvent(event SessionStreamStructuredMessageEvent) sessionTranscriptGetResponse {
	return sessionTranscriptGetResponse{
		ID:                 event.ID,
		Template:           event.Template,
		Provider:           event.Provider,
		Format:             event.Format,
		SchemaVersion:      event.SchemaVersion,
		Operation:          event.Operation,
		ResetReason:        event.ResetReason,
		History:            event.History,
		StructuredMessages: structuredMessagesField(event.StructuredMessages),
		Pagination:         event.Pagination,
	}
}

func structuredResetUpdate(current SessionStreamStructuredMessageEvent, reason string) *SessionStreamStructuredMessageEvent {
	current.Operation = sessionStructuredOperationReset
	current.ResetReason = reason
	current.StructuredMessages = nonNilStructuredMessages(current.StructuredMessages)
	return &current
}

func cloneStructuredStreamProjection(projection SessionStreamStructuredMessageEvent, resumeToken string) SessionStreamStructuredMessageEvent {
	projection.Operation = ""
	projection.ResetReason = ""
	projection.StructuredMessages = append([]SessionStructuredMessage(nil), projection.StructuredMessages...)
	if projection.StructuredMessages == nil {
		projection.StructuredMessages = []SessionStructuredMessage{}
	}
	if projection.History == nil {
		projection.History = &SessionStructuredHistory{}
	} else {
		history := *projection.History
		projection.History = &history
	}
	projection.History.Cursor.ResumeToken = resumeToken
	return projection
}

func structuredResumeToken(projection SessionStreamStructuredMessageEvent, includeThinking bool) sessionStructuredResumeTokenV1 {
	messages := nonNilStructuredMessages(projection.StructuredMessages)
	suffixWindow := projection.Pagination != nil && projection.Pagination.HasOlderMessages
	afterEntryID := ""
	if len(messages) > 0 {
		afterEntryID = messages[len(messages)-1].ID
	} else if suffixWindow && projection.History != nil {
		afterEntryID = projection.History.Cursor.AfterEntryID
	}
	prefixEnd := len(messages)
	if prefixEnd > 0 {
		prefixEnd--
	}
	projectionForHash := projection
	if suffixWindow {
		projectionForHash.Pagination = nil
	}
	return sessionStructuredResumeTokenV1{
		Version:          1,
		StreamSHA256:     structuredStreamIdentityHash(projection.History),
		AfterEntryID:     afterEntryID,
		MessageCount:     len(messages),
		PrefixSHA256:     hashStructuredMessages(messages[:prefixEnd]),
		ProjectionSHA256: hashStructuredProjection(projectionForHash, includeThinking),
		IncludeThinking:  includeThinking,
		SuffixWindow:     suffixWindow,
	}
}

func structuredStreamIdentityHash(history *SessionStructuredHistory) string {
	if history == nil {
		return sha256Hex(nil)
	}
	identity := history.TranscriptStreamID + "\x00" + history.ProviderSessionID + "\x00" + history.LogicalConversationID
	return sha256Hex([]byte(identity))
}

func hashStructuredProjection(projection SessionStreamStructuredMessageEvent, includeThinking bool) string {
	projection.Operation = ""
	projection.ResetReason = ""
	projection.StructuredMessages = nonNilStructuredMessages(projection.StructuredMessages)
	if projection.History != nil {
		history := *projection.History
		history.Cursor.ResumeToken = ""
		// Generation currently carries file observation evidence (mtime:size),
		// which changes on an ordinary append. It is not transcript identity.
		history.Generation = SessionStructuredGeneration{}
		projection.History = &history
	}
	digestInput := struct {
		Projection      SessionStreamStructuredMessageEvent `json:"projection"`
		IncludeThinking bool                                `json:"include_thinking"`
	}{Projection: projection, IncludeThinking: includeThinking}
	data, err := json.Marshal(digestInput)
	if err != nil {
		return sha256Hex(nil)
	}
	return sha256Hex(data)
}

func hashStructuredMessages(messages []SessionStructuredMessage) string {
	data, err := json.Marshal(nonNilStructuredMessages(messages))
	if err != nil {
		return sha256Hex(nil)
	}
	return sha256Hex(data)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func encodeStructuredResumeToken(token sessionStructuredResumeTokenV1) string {
	data, err := json.Marshal(token)
	if err != nil {
		return ""
	}
	return sessionStructuredResumeTokenPrefix + base64.RawURLEncoding.EncodeToString(data)
}

func decodeStructuredResumeToken(encoded string) (sessionStructuredResumeTokenV1, bool) {
	var token sessionStructuredResumeTokenV1
	encoded = strings.TrimSpace(encoded)
	if len(encoded) > sessionStructuredResumeTokenMaxLen || !strings.HasPrefix(encoded, sessionStructuredResumeTokenPrefix) {
		return token, false
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, sessionStructuredResumeTokenPrefix))
	if err != nil {
		return token, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil {
		return token, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return token, false
	}
	if token.Version != 1 || token.MessageCount < 0 || token.StreamSHA256 == "" || token.PrefixSHA256 == "" || token.ProjectionSHA256 == "" {
		return token, false
	}
	if token.SuffixWindow && token.AfterEntryID == "" {
		return token, false
	}
	if !token.SuffixWindow && (token.MessageCount == 0) != (token.AfterEntryID == "") {
		return token, false
	}
	return token, true
}
