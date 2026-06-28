package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

type fakeAgentMailReplySender struct {
	req  AgentMailReplyRequest
	sent *AgentMailSentMessage
}

func (f *fakeAgentMailReplySender) Reply(_ context.Context, req AgentMailReplyRequest) (*AgentMailSentMessage, error) {
	f.req = req
	return f.sent, nil
}

func TestReplyToLetterRequiresConfiguredSender(t *testing.T) {
	svc := NewMailboxService(&fakeMailboxConvertStore{})

	_, err := svc.ReplyToLetter(context.Background(), "u1", "letter-1", "hello")
	if err == nil {
		t.Fatalf("expected not configured error")
	}
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "FEATURE_NOT_CONFIGURED" {
		t.Fatalf("error = %v, want FEATURE_NOT_CONFIGURED", err)
	}
}

func TestReplyToLetterSendsAndStoresOutboundLetter(t *testing.T) {
	threadID := "thread-1"
	sourceRef := "<inbound-rfc@example.com>"
	sentAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := &fakeMailboxConvertStore{letter: &model.MailboxLetter{
		ID:         "letter-1",
		UserID:     "u1",
		SourceType: model.MailboxSourceAgentMail,
		SourceRef:  &sourceRef,
		Direction:  model.MailboxDirectionInbound,
		ThreadID:   &threadID,
		Title:      "Hello",
		Metadata:   model.MailboxJSON(`{"bot_uid":"bot-a","mail_address":"bot@agent.qq.com","rfc_message_id":"<inbound-rfc@example.com>"}`),
	}}
	sender := &fakeAgentMailReplySender{sent: &AgentMailSentMessage{
		ID:           "provider-out-1",
		RFCMessageID: "<out-rfc@example.com>",
		ThreadID:     "thread-1",
		SentAt:       &sentAt,
	}}
	svc := NewMailboxService(store)
	svc.ConfigureAgentMailReplySender(sender)

	res, err := svc.ReplyToLetter(context.Background(), "u1", "letter-1", "  thanks  ")
	if err != nil {
		t.Fatalf("reply failed: %v", err)
	}
	if sender.req.UserID != "u1" || sender.req.BotUID != "bot-a" || sender.req.MailAddress != "bot@agent.qq.com" ||
		sender.req.LetterID != "letter-1" || sender.req.SourceRef != sourceRef || sender.req.ThreadID != threadID ||
		sender.req.RFCMessageID != sourceRef || sender.req.Content != "thanks" {
		t.Fatalf("unexpected sender request: %+v", sender.req)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("upsert count = %d, want 1", len(store.upserted))
	}
	out := store.upserted[0]
	if res.Letter != out {
		t.Fatalf("result letter not outbound upsert")
	}
	if out.SourceType != model.MailboxSourceAgentMail || out.Direction != model.MailboxDirectionOutbound {
		t.Fatalf("unexpected outbound routing: %+v", out)
	}
	if out.SourceRef == nil || *out.SourceRef != "<out-rfc@example.com>" {
		t.Fatalf("source ref = %v, want outbound rfc", out.SourceRef)
	}
	if out.ThreadID == nil || *out.ThreadID != threadID {
		t.Fatalf("thread = %v, want %s", out.ThreadID, threadID)
	}
	if out.Title != "Re: Hello" || out.BodyText == nil || *out.BodyText != "thanks" {
		t.Fatalf("unexpected content: title=%q body=%v", out.Title, out.BodyText)
	}
	if !out.CreatedAt.Equal(sentAt) {
		t.Fatalf("created_at = %s, want sent_at", out.CreatedAt)
	}
	if !strings.Contains(string(out.Metadata), `"reply_to_letter_id":"letter-1"`) ||
		!strings.Contains(string(out.Metadata), `"provider_message_id":"provider-out-1"`) ||
		!strings.Contains(string(out.Metadata), `"rfc_message_id":"\u003cout-rfc@example.com\u003e"`) {
		t.Fatalf("metadata missing reply fields: %s", string(out.Metadata))
	}
}

func TestReplyToLetterRejectsSystemLetter(t *testing.T) {
	store := &fakeMailboxConvertStore{letter: &model.MailboxLetter{
		ID:         "letter-1",
		UserID:     "u1",
		SourceType: model.MailboxSourceSystem,
		Title:      "System",
	}}
	sender := &fakeAgentMailReplySender{sent: &AgentMailSentMessage{ID: "out-1"}}
	svc := NewMailboxService(store)
	svc.ConfigureAgentMailReplySender(sender)

	_, err := svc.ReplyToLetter(context.Background(), "u1", "letter-1", "thanks")
	if err == nil {
		t.Fatalf("expected system letter reply to fail")
	}
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "VALIDATION_ERROR" {
		t.Fatalf("error = %v, want validation error", err)
	}
	if len(store.upserted) != 0 {
		t.Fatalf("unexpected outbound write")
	}
}
