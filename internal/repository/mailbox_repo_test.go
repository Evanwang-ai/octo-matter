package repository

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

func TestMailboxRepo_UpsertDoesNotResurrectDeletedLetters(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	sourceRef := "msg-1"
	mock.ExpectExec(`(?s)ON DUPLICATE KEY UPDATE.*metadata = VALUES\(metadata\),\s*updated_at = VALUES\(updated_at\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	r := &MailboxRepo{runner: sess}
	if err := r.Upsert(context.Background(), &model.MailboxLetter{
		UserID:     "u-1",
		SourceType: model.MailboxSourceAgentMail,
		SourceRef:  &sourceRef,
		Direction:  model.MailboxDirectionInbound,
		Title:      "hello",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMailboxRepo_ListUnreadExcludesArchived(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	now := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "user_id", "source_type", "source_ref", "direction", "thread_id", "title", "snippet",
		"body_text", "body_html", "from_name", "from_email", "metadata", "read_at", "archived_at",
		"deleted_at", "created_at", "updated_at",
	}).AddRow(
		"letter-1", "u-1", string(model.MailboxSourceAgentMail), "msg-1", string(model.MailboxDirectionInbound), nil,
		"hello", nil, nil, nil, nil, nil, nil, nil, nil, nil, now, now,
	)
	mock.ExpectQuery(`SELECT \* FROM mailbox_letters WHERE \(user_id = 'u-1' AND deleted_at IS NULL\) AND \(read_at IS NULL AND archived_at IS NULL\)`).
		WillReturnRows(rows)

	r := &MailboxRepo{runner: sess}
	got, hasMore, err := r.ListByUser(context.Background(), "u-1", MailboxFilter{Status: "unread", Limit: 50})
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if hasMore {
		t.Fatalf("hasMore = true, want false")
	}
	if len(got) != 1 || got[0].ID != "letter-1" {
		t.Fatalf("unexpected letters: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
