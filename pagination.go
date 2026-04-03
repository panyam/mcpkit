package mcpkit

import (
	"encoding/base64"
	"fmt"
	"strconv"
)

// defaultPageSize is the default number of items per page.
// 0 means return all items (no pagination).
const defaultPageSize = 0

// paginate applies cursor-based pagination to a slice of items.
// The cursor is an opaque base64-encoded offset. If cursor is empty, starts from
// the beginning. If pageSize is 0, returns all items with no nextCursor.
// Returns the page of items and a nextCursor (empty string if no more pages).
func paginate[T any](items []T, cursor string, pageSize int) ([]T, string, error) {
	if pageSize <= 0 || len(items) == 0 {
		return items, "", nil
	}

	offset := 0
	if cursor != "" {
		decoded, err := base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		offset, err = strconv.Atoi(string(decoded))
		if err != nil || offset < 0 {
			return nil, "", fmt.Errorf("invalid cursor")
		}
	}

	if offset >= len(items) {
		return nil, "", nil
	}

	end := offset + pageSize
	if end > len(items) {
		end = len(items)
	}

	page := items[offset:end]

	var nextCursor string
	if end < len(items) {
		nextCursor = base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}

	return page, nextCursor, nil
}
