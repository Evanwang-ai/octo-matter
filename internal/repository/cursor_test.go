package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

func TestCursor_RoundTrip(t *testing.T) {
	in := Cursor{
		CreatedAt: time.Date(2026, 4, 25, 14, 30, 45, 123456789, time.UTC),
		ID:        "abc-123",
	}
	s := EncodeCursor(in)
	if s == "" {
		t.Fatal("EncodeCursor returned empty string")
	}
	out, err := DecodeCursor(s)
	if err != nil {
		t.Fatalf("DecodeCursor failed: %v", err)
	}
	if !out.CreatedAt.Equal(in.CreatedAt) || out.ID != in.ID {
		t.Errorf("round trip mismatch: in=%+v out=%+v", in, out)
	}
}

func TestDecodeCursor_Rejects(t *testing.T) {
	cases := []string{
		"",
		"not-base64!",
		"Zm9v",         // valid base64 but no "|"
		"fG5vLXRz",     // "|no-ts" — empty nano part, parse fails
		"MTIzNDU2Nnw=", // "1234566|" — empty id
	}
	for _, s := range cases {
		if _, err := DecodeCursor(s); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeCursor(%q): got %v, want ErrInvalidCursor", s, err)
		}
	}
}

func TestMatterListCursor_RoundTripPriority(t *testing.T) {
	m := &model.Matter{
		ID:        "m-1",
		Priority:  model.MatterPriorityHigh,
		CreatedAt: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
	}
	s := EncodeMatterCursor("priority", "asc", m)
	if s == "" {
		t.Fatal("EncodeMatterCursor returned empty string")
	}
	out, err := DecodeListCursor(s)
	if err != nil {
		t.Fatalf("DecodeListCursor failed: %v", err)
	}
	if out.OrderBy != "priority" || out.OrderDir != "asc" || out.Value != "2" || out.ID != "m-1" {
		t.Fatalf("priority cursor mismatch: %+v", out)
	}
}

func TestMatterListCursor_NullDeadline(t *testing.T) {
	m := &model.Matter{ID: "m-null"}
	s := EncodeMatterCursor("deadline", "asc", m)
	out, err := DecodeListCursor(s)
	if err != nil {
		t.Fatalf("DecodeListCursor failed: %v", err)
	}
	if out.OrderBy != "deadline" || !out.IsNull || out.ID != "m-null" {
		t.Fatalf("deadline null cursor mismatch: %+v", out)
	}
}

func TestDecodeListCursor_AcceptsLegacyCreatedAtCursor(t *testing.T) {
	in := Cursor{
		CreatedAt: time.Date(2026, 4, 25, 14, 30, 45, 123456789, time.UTC),
		ID:        "abc-123",
	}
	out, err := DecodeListCursor(EncodeCursor(in))
	if err != nil {
		t.Fatalf("DecodeListCursor legacy failed: %v", err)
	}
	if out.OrderBy != "created_at" || out.OrderDir != "desc" || out.ID != in.ID {
		t.Fatalf("legacy list cursor mismatch: %+v", out)
	}
}
