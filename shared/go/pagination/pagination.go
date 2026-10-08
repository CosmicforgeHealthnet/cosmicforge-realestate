// Package pagination implements the platform cursor pagination contract
// (API_STANDARD.md):
//
//	{"data": [...], "page": {"next_cursor": null, "has_more": false}}
//
// Cursors are opaque base64url-encoded JSON objects holding the sort keys of
// the last returned row. They carry no authorization meaning; handlers must
// still apply the caller's filters on every page.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"strconv"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

// Defaults.
const (
	DefaultLimit = 20
	MaxLimit     = 100
	maxCursorLen = 1024
)

// Page is the pagination metadata.
type Page struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// Response is a paginated collection.
type Response[T any] struct {
	Data []T  `json:"data"`
	Page Page `json:"page"`
}

// Limit parses a limit query parameter bounded to [1, max].
func Limit(raw string, def, max int) (int, error) {
	if def <= 0 {
		def = DefaultLimit
	}
	if max <= 0 {
		max = MaxLimit
	}
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, apperrors.InvalidRequest("limit must be a positive integer.",
			apperrors.Detail{Field: "limit", Code: "INVALID", Message: "limit must be a positive integer"})
	}
	if n > max {
		n = max
	}
	return n, nil
}

// Encode serialises cursor state.
func Encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Decode parses a cursor into v. Empty cursors leave v untouched and return false.
func Decode(cursor string, v any) (bool, error) {
	if cursor == "" {
		return false, nil
	}
	invalid := apperrors.InvalidRequest("cursor is invalid.",
		apperrors.Detail{Field: "cursor", Code: "INVALID", Message: "cursor is invalid"})
	if len(cursor) > maxCursorLen {
		return false, invalid
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return false, invalid
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, invalid
	}
	return true, nil
}

// Build trims an over-fetched slice (query limit+1 rows) and computes the
// next cursor from the last returned item.
func Build[T any](rows []T, limit int, cursorOf func(T) any) (Response[T], error) {
	resp := Response[T]{Data: rows, Page: Page{}}
	if resp.Data == nil {
		resp.Data = []T{}
	}
	if len(rows) > limit {
		resp.Data = rows[:limit]
		resp.Page.HasMore = true
		c, err := Encode(cursorOf(resp.Data[limit-1]))
		if err != nil {
			return Response[T]{}, err
		}
		resp.Page.NextCursor = &c
	}
	return resp, nil
}
