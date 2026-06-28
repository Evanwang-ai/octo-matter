package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

type agentMailSyncBindingStore interface {
	ListActiveForSync(context.Context, int) ([]*model.AgentMailBinding, error)
	MarkSyncSuccess(context.Context, string, *string) error
	MarkSyncError(context.Context, string, string) error
}

type mailboxLetterWriter interface {
	Upsert(context.Context, *model.MailboxLetter) error
}

type AgentMailClient interface {
	ListMessages(context.Context, *model.AgentMailBinding) (*AgentMailMessageBatch, error)
}

type AgentMailMessageBatch struct {
	Messages   []AgentMailMessage
	NextCursor *string
}

type AgentMailMessage struct {
	ID           string
	RFCMessageID string
	ThreadID     string
	Subject      string
	Snippet      string
	BodyText     string
	BodyHTML     string
	FromName     string
	FromEmail    string
	ReceivedAt   *time.Time
}

type AgentMailSyncService struct {
	bindings agentMailSyncBindingStore
	letters  mailboxLetterWriter
	client   AgentMailClient
}

func NewAgentMailSyncService(bindings agentMailSyncBindingStore, letters mailboxLetterWriter, client AgentMailClient) *AgentMailSyncService {
	return &AgentMailSyncService{
		bindings: bindings,
		letters:  letters,
		client:   client,
	}
}

func (s *AgentMailSyncService) SyncOnce(ctx context.Context, limit int) (int, error) {
	if s == nil || s.bindings == nil || s.letters == nil || s.client == nil {
		return 0, nil
	}
	bindings, err := s.bindings.ListActiveForSync(ctx, limit)
	if err != nil {
		return 0, err
	}
	var synced int
	var firstErr error
	for _, binding := range bindings {
		count, err := s.syncBinding(ctx, binding)
		synced += count
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return synced, firstErr
}

func (s *AgentMailSyncService) syncBinding(ctx context.Context, binding *model.AgentMailBinding) (int, error) {
	if binding == nil {
		return 0, nil
	}
	if !isSyncableAgentMailBinding(binding) {
		err := errors.New("invalid agent mail binding")
		if strings.TrimSpace(binding.ID) != "" {
			_ = s.bindings.MarkSyncError(ctx, binding.ID, truncateSyncError(err.Error()))
		}
		return 0, err
	}
	batch, err := s.client.ListMessages(ctx, binding)
	if err != nil {
		_ = s.bindings.MarkSyncError(ctx, binding.ID, truncateSyncError(err.Error()))
		return 0, err
	}
	if batch == nil {
		err := errors.New("empty agent mail batch")
		_ = s.bindings.MarkSyncError(ctx, binding.ID, truncateSyncError(err.Error()))
		return 0, err
	}
	var count int
	for _, msg := range batch.Messages {
		letter, ok := buildAgentMailLetter(binding, msg)
		if !ok {
			continue
		}
		if err := s.letters.Upsert(ctx, letter); err != nil {
			_ = s.bindings.MarkSyncError(ctx, binding.ID, truncateSyncError(err.Error()))
			return count, err
		}
		count++
	}
	if err := s.bindings.MarkSyncSuccess(ctx, binding.ID, batch.NextCursor); err != nil {
		return count, err
	}
	return count, nil
}

func buildAgentMailLetter(binding *model.AgentMailBinding, msg AgentMailMessage) (*model.MailboxLetter, bool) {
	if !isSyncableAgentMailBinding(binding) {
		return nil, false
	}
	sourceRef := strings.TrimSpace(msg.RFCMessageID)
	if sourceRef == "" {
		sourceRef = strings.TrimSpace(msg.ID)
	}
	if sourceRef == "" {
		return nil, false
	}
	title := strings.TrimSpace(msg.Subject)
	if title == "" {
		title = "(no subject)"
	}

	cleanHTML := ""
	plainText := normalizeMailboxText(msg.BodyText)
	snippet := strings.TrimSpace(msg.Snippet)
	if strings.TrimSpace(msg.BodyHTML) != "" {
		cleanHTML, plainText, snippet = SanitizeEmailHTML(msg.BodyHTML)
	} else if snippet == "" {
		snippet = mailboxSnippet(plainText, 200)
	}

	metadata, _ := json.Marshal(map[string]any{
		"bot_uid":             binding.BotUID,
		"mail_address":        binding.MailAddress,
		"provider_message_id": strings.TrimSpace(msg.ID),
		"rfc_message_id":      strings.TrimSpace(msg.RFCMessageID),
		"thread_id":           strings.TrimSpace(msg.ThreadID),
		"received_at":         agentMailTimeString(msg.ReceivedAt),
	})

	letter := &model.MailboxLetter{
		UserID:     binding.UserID,
		SourceType: model.MailboxSourceAgentMail,
		SourceRef:  &sourceRef,
		Direction:  model.MailboxDirectionInbound,
		ThreadID:   optionalStringPtr(strings.TrimSpace(msg.ThreadID)),
		Title:      title,
		Snippet:    optionalStringPtr(snippet),
		BodyText:   optionalStringPtr(plainText),
		BodyHTML:   optionalStringPtr(cleanHTML),
		FromName:   optionalStringPtr(strings.TrimSpace(msg.FromName)),
		FromEmail:  optionalStringPtr(strings.TrimSpace(msg.FromEmail)),
		Metadata:   model.MailboxJSON(metadata),
	}
	if msg.ReceivedAt != nil {
		letter.CreatedAt = *msg.ReceivedAt
	}
	return letter, true
}

func isSyncableAgentMailBinding(binding *model.AgentMailBinding) bool {
	if binding == nil {
		return false
	}
	return strings.TrimSpace(binding.ID) != "" &&
		strings.TrimSpace(binding.UserID) != "" &&
		strings.TrimSpace(binding.BotUID) != "" &&
		isValidAgentMailAddress(binding.MailAddress)
}

func agentMailTimeString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func truncateSyncError(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= 500 {
		return s
	}
	rs := []rune(s)
	return string(rs[:500])
}
