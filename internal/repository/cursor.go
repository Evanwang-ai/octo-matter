package repository

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

// ErrInvalidCursor indicates a malformed pagination cursor. Callers should
// treat it as a 400-class error; corrupting a cursor never reveals data.
var ErrInvalidCursor = errors.New("invalid cursor")

// Cursor is the composite pagination position used by ListBySpace. Ordering
// is by (created_at DESC, id DESC); the id break-tie is required because
// created_at is stored at second-or-microsecond resolution and collisions
// at the page boundary would otherwise skip or repeat rows.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// ListCursor is the ordered pagination position for Matter list ordering.
// Value is encoded as a string so it can carry timestamps, strings, numbers,
// and floats without exposing SQL details to clients.
type ListCursor struct {
	OrderBy  string `json:"order_by"`
	OrderDir string `json:"order_dir"`
	Value    string `json:"value,omitempty"`
	IsNull   bool   `json:"is_null,omitempty"`
	ID       string `json:"id"`
}

// EncodeCursor serializes a Cursor to an opaque URL-safe string. Format is
// base64("<unix-nano>|<uuid>"); clients must treat it as opaque.
func EncodeCursor(c Cursor) string {
	raw := fmt.Sprintf("%d|%s", c.CreatedAt.UnixNano(), c.ID)
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses the string produced by EncodeCursor. Returns
// ErrInvalidCursor if the input is not a cursor we emitted — callers should
// reject the request rather than silently falling back to "no cursor".
func DecodeCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, ErrInvalidCursor
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return Cursor{}, ErrInvalidCursor
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{
		CreatedAt: time.Unix(0, ns),
		ID:        parts[1],
	}, nil
}

func EncodeMatterCursor(orderBy, orderDir string, m *model.Matter) string {
	order, ok := NormalizeMatterOrder(orderBy, orderDir)
	if !ok || m == nil {
		return ""
	}
	cur := ListCursor{OrderBy: order.By, OrderDir: order.Dir, ID: m.ID}
	switch order.By {
	case "created_at":
		cur.Value = strconv.FormatInt(m.CreatedAt.UnixNano(), 10)
	case "updated_at":
		cur.Value = strconv.FormatInt(m.UpdatedAt.UnixNano(), 10)
	case "deadline":
		if m.Deadline == nil {
			cur.IsNull = true
		} else {
			cur.Value = strconv.FormatInt(m.Deadline.UnixNano(), 10)
		}
	case "priority":
		rank := int(m.Priority)
		if rank == 0 {
			rank = 5
		}
		cur.Value = strconv.Itoa(rank)
	case "manual":
		if m.SortOrder == nil {
			cur.IsNull = true
		} else {
			cur.Value = strconv.FormatFloat(*m.SortOrder, 'g', -1, 64)
		}
	case "title":
		cur.Value = m.Title
	case "seq_no":
		cur.Value = strconv.Itoa(m.SeqNo)
	}
	raw, _ := json.Marshal(cur)
	return base64.URLEncoding.EncodeToString(raw)
}

func DecodeListCursor(s string) (ListCursor, error) {
	if s == "" {
		return ListCursor{}, ErrInvalidCursor
	}
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return ListCursor{}, ErrInvalidCursor
	}
	if len(b) > 0 && b[0] == '{' {
		var cur ListCursor
		if err := json.Unmarshal(b, &cur); err != nil {
			return ListCursor{}, ErrInvalidCursor
		}
		if cur.ID == "" {
			return ListCursor{}, ErrInvalidCursor
		}
		order, ok := NormalizeMatterOrder(cur.OrderBy, cur.OrderDir)
		if !ok {
			return ListCursor{}, ErrInvalidCursor
		}
		cur.OrderBy, cur.OrderDir = order.By, order.Dir
		return cur, nil
	}
	legacy, err := DecodeCursor(s)
	if err != nil {
		return ListCursor{}, err
	}
	return ListCursor{
		OrderBy:  "created_at",
		OrderDir: "desc",
		Value:    strconv.FormatInt(legacy.CreatedAt.UnixNano(), 10),
		ID:       legacy.ID,
	}, nil
}
