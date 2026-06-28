package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

type fakeAgentMailSyncBindingStore struct {
	items         []*model.AgentMailBinding
	successID     string
	successCursor *string
	errorID       string
	errorMessage  string
}

func (f *fakeAgentMailSyncBindingStore) ListActiveForSync(context.Context, int) ([]*model.AgentMailBinding, error) {
	return f.items, nil
}

func (f *fakeAgentMailSyncBindingStore) MarkSyncSuccess(_ context.Context, id string, cursor *string) error {
	f.successID = id
	f.successCursor = cursor
	return nil
}

func (f *fakeAgentMailSyncBindingStore) MarkSyncError(_ context.Context, id, message string) error {
	f.errorID = id
	f.errorMessage = message
	return nil
}

type fakeMailboxLetterWriter struct {
	items []*model.MailboxLetter
	err   error
}

func (f *fakeMailboxLetterWriter) Upsert(_ context.Context, letter *model.MailboxLetter) error {
	if f.err != nil {
		return f.err
	}
	f.items = append(f.items, letter)
	return nil
}

type fakeAgentMailClient struct {
	batch *AgentMailMessageBatch
	err   error
}

func (f *fakeAgentMailClient) ListMessages(context.Context, *model.AgentMailBinding) (*AgentMailMessageBatch, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.batch, nil
}

func TestAgentMailSyncStoresSanitizedMessages(t *testing.T) {
	receivedAt := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	nextCursor := "cursor-2"
	bindings := &fakeAgentMailSyncBindingStore{items: []*model.AgentMailBinding{{
		ID:                   "bind-1",
		UserID:               "u1",
		BotUID:               "bot-a",
		MailAddress:          "bot@agent.qq.com",
		CredentialsEncrypted: []byte("opaque"),
		SyncStatus:           model.AgentMailSyncActive,
	}}}
	letters := &fakeMailboxLetterWriter{}
	client := &fakeAgentMailClient{batch: &AgentMailMessageBatch{
		NextCursor: &nextCursor,
		Messages: []AgentMailMessage{{
			ID:           "provider-1",
			RFCMessageID: "<rfc-1@example.com>",
			ThreadID:     "thread-1",
			Subject:      "  Hello  ",
			BodyHTML:     `<div onclick="x()">Hi <script>alert(1)</script><a href="javascript:bad()">bad</a><a href="https://example.com">ok</a></div>`,
			FromName:     "Agent Mail",
			FromEmail:    "bot@agent.qq.com",
			ReceivedAt:   &receivedAt,
		}},
	}}

	svc := NewAgentMailSyncService(bindings, letters, client)
	count, err := svc.SyncOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("sync count = %d, want 1", count)
	}
	if bindings.successID != "bind-1" || bindings.successCursor == nil || *bindings.successCursor != nextCursor {
		t.Fatalf("unexpected success mark: id=%q cursor=%v", bindings.successID, bindings.successCursor)
	}
	if len(letters.items) != 1 {
		t.Fatalf("upsert count = %d, want 1", len(letters.items))
	}
	letter := letters.items[0]
	if letter.UserID != "u1" || letter.SourceType != model.MailboxSourceAgentMail || letter.SourceRef == nil || *letter.SourceRef != "<rfc-1@example.com>" {
		t.Fatalf("unexpected letter routing: %+v", letter)
	}
	if letter.Title != "Hello" {
		t.Fatalf("title = %q, want Hello", letter.Title)
	}
	if letter.Direction != model.MailboxDirectionInbound || letter.ThreadID == nil || *letter.ThreadID != "thread-1" {
		t.Fatalf("unexpected direction/thread: direction=%q thread=%v", letter.Direction, letter.ThreadID)
	}
	if letter.BodyHTML == nil || strings.Contains(*letter.BodyHTML, "script") || strings.Contains(*letter.BodyHTML, "onclick") || strings.Contains(*letter.BodyHTML, "javascript:") {
		t.Fatalf("body html was not sanitized: %v", letter.BodyHTML)
	}
	if letter.BodyText == nil || *letter.BodyText != "Hi bad ok" {
		t.Fatalf("body text = %v, want sanitized plain text", letter.BodyText)
	}
	if letter.Snippet == nil || *letter.Snippet != "Hi bad ok" {
		t.Fatalf("snippet = %v, want sanitized snippet", letter.Snippet)
	}
	if letter.FromEmail == nil || *letter.FromEmail != "bot@agent.qq.com" {
		t.Fatalf("from email = %v, want bot@agent.qq.com", letter.FromEmail)
	}
	if !strings.Contains(string(letter.Metadata), `"bot_uid":"bot-a"`) ||
		!strings.Contains(string(letter.Metadata), `"provider_message_id":"provider-1"`) ||
		!strings.Contains(string(letter.Metadata), `"thread_id":"thread-1"`) {
		t.Fatalf("metadata missing agent mail fields: %s", string(letter.Metadata))
	}
	if !letter.CreatedAt.Equal(receivedAt) {
		t.Fatalf("created_at = %s, want received_at", letter.CreatedAt)
	}
}

func TestAgentMailSyncMarksClientError(t *testing.T) {
	bindings := &fakeAgentMailSyncBindingStore{items: []*model.AgentMailBinding{{
		ID: "bind-1",
	}}}
	letters := &fakeMailboxLetterWriter{}
	client := &fakeAgentMailClient{err: errors.New("temporary upstream failure")}

	svc := NewAgentMailSyncService(bindings, letters, client)
	count, err := svc.SyncOnce(context.Background(), 10)
	if err == nil {
		t.Fatalf("expected sync error")
	}
	if count != 0 || len(letters.items) != 0 {
		t.Fatalf("unexpected writes: count=%d writes=%d", count, len(letters.items))
	}
	if bindings.errorID != "bind-1" || bindings.errorMessage != "temporary upstream failure" {
		t.Fatalf("unexpected error mark: id=%q msg=%q", bindings.errorID, bindings.errorMessage)
	}
}

func TestAgentMailSyncDisabledWithoutClient(t *testing.T) {
	bindings := &fakeAgentMailSyncBindingStore{items: []*model.AgentMailBinding{{ID: "bind-1"}}}
	letters := &fakeMailboxLetterWriter{}

	svc := NewAgentMailSyncService(bindings, letters, nil)
	count, err := svc.SyncOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("disabled sync returned error: %v", err)
	}
	if count != 0 || len(letters.items) != 0 || bindings.successID != "" || bindings.errorID != "" {
		t.Fatalf("disabled sync did work: count=%d writes=%d success=%q error=%q", count, len(letters.items), bindings.successID, bindings.errorID)
	}
}
