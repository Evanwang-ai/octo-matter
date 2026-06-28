package repository

import (
	"context"
	"testing"

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
