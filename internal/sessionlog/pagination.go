package sessionlog

import (
	"errors"
	"fmt"
)

// CursorDirection identifies which side of a transcript cursor was requested.
type CursorDirection string

const (
	// CursorDirectionBefore requests entries before the cursor entry.
	CursorDirectionBefore CursorDirection = "before"
	// CursorDirectionAfter requests entries after the cursor entry.
	CursorDirectionAfter CursorDirection = "after"
)

// ErrCursorNotFound reports a transcript cursor that is absent from the
// current provider transcript view.
var ErrCursorNotFound = errors.New("transcript cursor not found")

// CursorNotFoundError identifies an invalidated transcript entry cursor.
type CursorNotFoundError struct {
	Direction CursorDirection
	EntryID   string
}

// Error implements error.
func (e *CursorNotFoundError) Error() string {
	return fmt.Sprintf("%s transcript cursor %q not found", e.Direction, e.EntryID)
}

// Unwrap exposes ErrCursorNotFound for errors.Is callers.
func (e *CursorNotFoundError) Unwrap() error {
	return ErrCursorNotFound
}

// ErrDuplicateEntryID reports provider output that cannot support an
// unambiguous entry-ID cursor.
var ErrDuplicateEntryID = errors.New("duplicate transcript entry ID")

// DuplicateEntryIDError identifies a repeated provider transcript entry ID.
type DuplicateEntryIDError struct {
	EntryID string
}

// Error implements error.
func (e *DuplicateEntryIDError) Error() string {
	return fmt.Sprintf("transcript entry ID %q appears more than once", e.EntryID)
}

// Unwrap exposes ErrDuplicateEntryID for errors.Is callers.
func (e *DuplicateEntryIDError) Unwrap() error {
	return ErrDuplicateEntryID
}

func readProviderFilePage(provider, path string, tailCompactions int, beforeEntryID, afterEntryID string, raw bool) (*Session, error) {
	if beforeEntryID == "" && afterEntryID == "" {
		if raw {
			return ReadProviderFileRaw(provider, path, tailCompactions)
		}
		return ReadProviderFile(provider, path, tailCompactions)
	}

	var (
		session *Session
		err     error
	)
	if raw {
		session, err = ReadProviderFileRaw(provider, path, 0)
	} else {
		session, err = ReadProviderFile(provider, path, 0)
	}
	if err != nil {
		return nil, err
	}
	return paginateSession(session, tailCompactions, beforeEntryID, afterEntryID)
}

func paginateSession(session *Session, tailCompactions int, beforeEntryID, afterEntryID string) (*Session, error) {
	if session == nil {
		return nil, fmt.Errorf("paginate transcript: session is nil")
	}
	if beforeEntryID != "" && afterEntryID != "" {
		return nil, fmt.Errorf("paginate transcript: before and after entry IDs are mutually exclusive")
	}

	seen, err := uniqueEntryIDs(session)
	if err != nil {
		return nil, err
	}

	direction := CursorDirectionBefore
	cursor := beforeEntryID
	if afterEntryID != "" {
		direction = CursorDirectionAfter
		cursor = afterEntryID
	}
	if cursor != "" {
		if _, found := seen[cursor]; !found {
			return nil, &CursorNotFoundError{Direction: direction, EntryID: cursor}
		}
	}

	paginated, info := sliceAtCompactBoundaries(session.Messages, tailCompactions, beforeEntryID, afterEntryID)
	info.PartialRead = session.Partial
	session.Messages = paginated
	session.Pagination = info
	return session, nil
}

// PageSession returns a paginated copy of an already parsed session. The
// source session and its complete message list remain unchanged so callers can
// derive transcript-wide state from the same file observation as the page.
func PageSession(session *Session, tailCompactions int, beforeEntryID, afterEntryID string) (*Session, error) {
	if session == nil {
		return nil, fmt.Errorf("paginate transcript: session is nil")
	}
	page := *session
	page.Messages = append([]*Entry(nil), session.Messages...)
	page.Pagination = nil
	return paginateSession(&page, tailCompactions, beforeEntryID, afterEntryID)
}

// LimitSession keeps at most limit entries of an already paginated session and
// corrects its pagination so the dropped side reads as "more exists". It runs
// after the before/after cursor and the tail-compactions cut, so a compaction
// segment is no longer the smallest window a reader can ask for.
//
// Which side it keeps depends on the direction of travel. A read with an
// after cursor is paging forward, so it keeps the OLDEST limit entries after
// the cursor and sets HasNewerMessages: keeping the newest would leave a gap
// between the cursor and the page that nothing reports. Every other read keeps
// the NEWEST limit entries and sets HasOlderMessages, with
// TruncatedBeforeMessage naming the first kept entry, the cursor for the next
// older page.
//
// limit <= 0 means no limit. The session is changed in place; callers that
// still need the full message list must pass a copy (PageSession returns one).
func LimitSession(session *Session, limit int, forward bool) {
	if session == nil || limit <= 0 || len(session.Messages) <= limit {
		return
	}
	info := session.Pagination
	if info == nil {
		info = &PaginationInfo{TotalMessageCount: len(session.Messages), PartialRead: session.Partial}
		session.Pagination = info
	}
	if forward {
		session.Messages = session.Messages[:limit]
		info.HasNewerMessages = true
	} else {
		session.Messages = session.Messages[len(session.Messages)-limit:]
		info.HasOlderMessages = true
		info.TruncatedBeforeMessage = session.Messages[0].UUID
	}
	info.ReturnedMessageCount = len(session.Messages)
}

func validateUniqueEntryIDs(session *Session) error {
	_, err := uniqueEntryIDs(session)
	return err
}

func uniqueEntryIDs(session *Session) (map[string]struct{}, error) {
	if session == nil {
		return nil, fmt.Errorf("validate transcript entry IDs: session is nil")
	}
	seen := make(map[string]struct{}, len(session.Messages))
	for _, entry := range session.Messages {
		if entry == nil || entry.UUID == "" {
			continue
		}
		if _, exists := seen[entry.UUID]; exists {
			return nil, &DuplicateEntryIDError{EntryID: entry.UUID}
		}
		seen[entry.UUID] = struct{}{}
	}
	return seen, nil
}
