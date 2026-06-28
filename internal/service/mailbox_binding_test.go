package service

import (
	"context"
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

type fakeAgentMailBindingStore struct {
	items []*model.AgentMailBinding
}

func (f *fakeAgentMailBindingStore) ListByUser(context.Context, string) ([]*model.AgentMailBinding, error) {
	return f.items, nil
}

func (f *fakeAgentMailBindingStore) Upsert(_ context.Context, b *model.AgentMailBinding) error {
	f.items = append(f.items, b)
	return nil
}

func (f *fakeAgentMailBindingStore) Delete(context.Context, string, string) error {
	return nil
}

func TestBindAgentMailValidatesOwnedBotAndAddress(t *testing.T) {
	store := &fakeAgentMailBindingStore{}
	svc := NewMailboxService(nil, store)

	if _, err := svc.BindAgentMail(context.Background(), "u1", []string{"bot-a"}, "bot-b", "bot@agent.qq.com"); err == nil {
		t.Fatalf("expected unowned bot binding to fail")
	}
	if _, err := svc.BindAgentMail(context.Background(), "u1", []string{"bot-a"}, "bot-a", "bot@example.com"); err == nil {
		t.Fatalf("expected non-agent.qq.com address to fail")
	}

	b, err := svc.BindAgentMail(context.Background(), "u1", []string{"bot-a"}, "bot-a", "BOT@agent.qq.com")
	if err != nil {
		t.Fatalf("bind failed: %v", err)
	}
	if b.UserID != "u1" || b.BotUID != "bot-a" || b.MailAddress != "bot@agent.qq.com" {
		t.Fatalf("unexpected binding: %+v", b)
	}
	if b.SyncStatus != model.AgentMailSyncPaused {
		t.Fatalf("sync status = %q, want paused", b.SyncStatus)
	}
	if len(store.items) != 1 {
		t.Fatalf("upsert count = %d, want 1", len(store.items))
	}
}

func TestActivateAgentMailBindingStoresEncryptedCredentials(t *testing.T) {
	store := &fakeAgentMailBindingStore{}
	svc := NewMailboxService(nil, store)
	cursor := "cursor-1"

	b, err := svc.ActivateAgentMailBinding(context.Background(), "u1", "bot-a", "BOT@agent.qq.com", []byte("encrypted"), &cursor)
	if err != nil {
		t.Fatalf("activate failed: %v", err)
	}
	if b.UserID != "u1" || b.BotUID != "bot-a" || b.MailAddress != "bot@agent.qq.com" {
		t.Fatalf("unexpected binding: %+v", b)
	}
	if b.SyncStatus != model.AgentMailSyncActive {
		t.Fatalf("sync status = %q, want active", b.SyncStatus)
	}
	if string(b.CredentialsEncrypted) != "encrypted" {
		t.Fatalf("credentials not stored as opaque bytes: %q", string(b.CredentialsEncrypted))
	}
	if b.SyncCursor == nil || *b.SyncCursor != "cursor-1" {
		t.Fatalf("cursor = %v, want cursor-1", b.SyncCursor)
	}
	if len(store.items) != 1 {
		t.Fatalf("upsert count = %d, want 1", len(store.items))
	}
}

func TestActivateAgentMailBindingRejectsInvalidInput(t *testing.T) {
	store := &fakeAgentMailBindingStore{}
	svc := NewMailboxService(nil, store)

	if _, err := svc.ActivateAgentMailBinding(context.Background(), "u1", "bot-a", "bot@example.com", []byte("encrypted"), nil); err == nil {
		t.Fatalf("expected non-Agent Mail address to fail")
	}
	if _, err := svc.ActivateAgentMailBinding(context.Background(), "u1", "bot-a", "bot@agent.qq.com", nil, nil); err == nil {
		t.Fatalf("expected empty credentials to fail")
	}
	if len(store.items) != 0 {
		t.Fatalf("invalid activation wrote %d bindings", len(store.items))
	}
}
