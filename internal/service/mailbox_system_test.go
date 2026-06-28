package service

import (
	"context"
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
)

func TestPushSystemLetterToUsersValidatesAllUsersBeforeWriting(t *testing.T) {
	store := &fakeMailboxConvertStore{}
	svc := NewMailboxService(store)

	err := svc.PushSystemLetterToUsers(context.Background(), []string{"u1", "   "}, "welcome", "Welcome", "<p>hi</p>")
	if err == nil {
		t.Fatalf("expected invalid user id to fail")
	}
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "VALIDATION_ERROR" {
		t.Fatalf("error = %v, want validation error", err)
	}
	if len(store.upserted) != 0 {
		t.Fatalf("system letter wrote before validating all users: %d writes", len(store.upserted))
	}
}

func TestPushSystemLetterToUsersDedupesUsers(t *testing.T) {
	store := &fakeMailboxConvertStore{}
	svc := NewMailboxService(store)

	if err := svc.PushSystemLetterToUsers(context.Background(), []string{" u1 ", "u1"}, "welcome", "Welcome", "<p>hi</p>"); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("upsert count = %d, want one deduped write", len(store.upserted))
	}
	if store.upserted[0].UserID != "u1" {
		t.Fatalf("user id = %q, want trimmed u1", store.upserted[0].UserID)
	}
}
