package service

import (
	"context"
	"encoding/json"
	"net/mail"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

type mailboxStore interface {
	ListByUser(context.Context, string, repository.MailboxFilter) ([]*model.MailboxLetter, bool, error)
	GetByID(context.Context, string, string) (*model.MailboxLetter, error)
	MarkRead(context.Context, string, string) error
	MarkAllRead(context.Context, string) error
	Archive(context.Context, string, string) error
	SoftDelete(context.Context, string, string) error
	BulkAction(context.Context, string, []string, string) error
	UnreadCount(context.Context, string) (int, error)
	Upsert(context.Context, *model.MailboxLetter) error
	UpdateMetadata(context.Context, string, string, model.MailboxJSON) error
}

type agentMailBindingStore interface {
	ListByUser(context.Context, string) ([]*model.AgentMailBinding, error)
	Upsert(context.Context, *model.AgentMailBinding) error
	Delete(context.Context, string, string) error
}

type MailboxService struct {
	repo       mailboxStore
	bindings   agentMailBindingStore
	conversion mailboxConversion
	reply      mailboxReplyConfig
}

type mailboxConversion struct {
	verifier mailboxSpaceCreateVerifier
	creator  mailboxMatterCreator
	v2       mailboxMatterCreatePreparer
}

type mailboxSpaceCreateVerifier interface {
	VerifyMatterCreate(context.Context, string, string, string) error
}

type mailboxMatterCreator interface {
	CreateMatterWithAssignees(context.Context, *model.Matter, []string) (*MatterDetail, error)
}

type mailboxMatterCreatePreparer interface {
	PrepareCreate(context.Context, *model.Matter, []string, string, string, bool, []string) (*model.Matter, error)
	AfterCreate(context.Context, *model.Matter, string, []string)
}

type mailboxReplyConfig struct {
	sender AgentMailReplySender
}

type AgentMailReplySender interface {
	Reply(context.Context, AgentMailReplyRequest) (*AgentMailSentMessage, error)
}

type AgentMailReplyRequest struct {
	UserID       string
	BotUID       string
	MailAddress  string
	LetterID     string
	SourceRef    string
	ThreadID     string
	RFCMessageID string
	Content      string
}

type AgentMailSentMessage struct {
	ID           string
	RFCMessageID string
	ThreadID     string
	SentAt       *time.Time
}

type MailboxConvertInput struct {
	LetterID    string
	SpaceID     string
	ProjectID   string
	LeaderUID   string
	Status      string
	AssigneeIDs []string
}

type MailboxConvertResult struct {
	Matter *MatterDetail        `json:"matter"`
	Letter *model.MailboxLetter `json:"letter"`
}

type MailboxReplyResult struct {
	Letter *model.MailboxLetter `json:"letter"`
}

func NewMailboxService(repo mailboxStore, bindings ...agentMailBindingStore) *MailboxService {
	s := &MailboxService{repo: repo}
	if len(bindings) > 0 {
		s.bindings = bindings[0]
	}
	return s
}

func (s *MailboxService) ConfigureMatterConversion(verifier mailboxSpaceCreateVerifier, creator mailboxMatterCreator, v2 mailboxMatterCreatePreparer) {
	s.conversion = mailboxConversion{verifier: verifier, creator: creator, v2: v2}
}

func (s *MailboxService) ConfigureAgentMailReplySender(sender AgentMailReplySender) {
	s.reply = mailboxReplyConfig{sender: sender}
}

func (s *MailboxService) List(ctx context.Context, userID string, filter repository.MailboxFilter) ([]*model.MailboxLetter, bool, string, error) {
	items, hasMore, err := s.repo.ListByUser(ctx, userID, filter)
	if err != nil {
		return nil, false, "", err
	}
	return items, hasMore, repository.NextMailboxCursor(items, hasMore), nil
}

func (s *MailboxService) Get(ctx context.Context, userID, id string) (*model.MailboxLetter, error) {
	return s.repo.GetByID(ctx, userID, id)
}

func (s *MailboxService) Update(ctx context.Context, userID, id, action string) error {
	switch action {
	case "mark_read":
		return s.repo.MarkRead(ctx, userID, id)
	case "archive":
		return s.repo.Archive(ctx, userID, id)
	default:
		return apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
}

func (s *MailboxService) MarkAllRead(ctx context.Context, userID string) error {
	return s.repo.MarkAllRead(ctx, userID)
}

func (s *MailboxService) Delete(ctx context.Context, userID, id string) error {
	return s.repo.SoftDelete(ctx, userID, id)
}

func (s *MailboxService) Bulk(ctx context.Context, userID string, ids []string, action string) error {
	return s.repo.BulkAction(ctx, userID, ids, action)
}

func (s *MailboxService) UnreadCount(ctx context.Context, userID string) (int, error) {
	return s.repo.UnreadCount(ctx, userID)
}

func (s *MailboxService) ListAgentMailBindings(ctx context.Context, userID string) ([]*model.AgentMailBinding, error) {
	if s.bindings == nil {
		return []*model.AgentMailBinding{}, nil
	}
	return s.bindings.ListByUser(ctx, userID)
}

func (s *MailboxService) BindAgentMail(ctx context.Context, userID string, ownedBotUIDs []string, botUID, mailAddress string) (*model.AgentMailBinding, error) {
	if s.bindings == nil {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	userID = strings.TrimSpace(userID)
	botUID = strings.TrimSpace(botUID)
	mailAddress = strings.ToLower(strings.TrimSpace(mailAddress))
	if userID == "" || botUID == "" || mailAddress == "" {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if !containsString(ownedBotUIDs, botUID) {
		return nil, apperr.Forbidden(i18n.KeyForbidden)
	}
	if !isValidAgentMailAddress(mailAddress) {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	b := &model.AgentMailBinding{
		UserID:      userID,
		BotUID:      botUID,
		MailAddress: mailAddress,
		SyncStatus:  model.AgentMailSyncPaused,
	}
	if err := s.bindings.Upsert(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *MailboxService) ActivateAgentMailBinding(ctx context.Context, userID, botUID, mailAddress string, credentialsEncrypted []byte, syncCursor *string) (*model.AgentMailBinding, error) {
	if s.bindings == nil {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	userID = strings.TrimSpace(userID)
	botUID = strings.TrimSpace(botUID)
	mailAddress = strings.ToLower(strings.TrimSpace(mailAddress))
	if userID == "" || botUID == "" || mailAddress == "" || len(credentialsEncrypted) == 0 || len(credentialsEncrypted) > 4096 {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if !isValidAgentMailAddress(mailAddress) {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if syncCursor != nil {
		trimmed := strings.TrimSpace(*syncCursor)
		if trimmed == "" {
			syncCursor = nil
		} else {
			syncCursor = &trimmed
		}
	}
	b := &model.AgentMailBinding{
		UserID:               userID,
		BotUID:               botUID,
		MailAddress:          mailAddress,
		CredentialsEncrypted: append([]byte(nil), credentialsEncrypted...),
		SyncCursor:           syncCursor,
		SyncStatus:           model.AgentMailSyncActive,
	}
	if err := s.bindings.Upsert(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *MailboxService) DeleteAgentMailBinding(ctx context.Context, userID, id string) error {
	if s.bindings == nil {
		return apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	return s.bindings.Delete(ctx, userID, id)
}

func (s *MailboxService) ConvertLetterToMatter(ctx context.Context, userID, callerToken string, in MailboxConvertInput) (*MailboxConvertResult, error) {
	if s.repo == nil || s.conversion.verifier == nil || s.conversion.creator == nil {
		return nil, apperr.FeatureNotConfigured(i18n.KeyInvalidRequest)
	}
	userID = strings.TrimSpace(userID)
	in.LetterID = strings.TrimSpace(in.LetterID)
	in.SpaceID = strings.TrimSpace(in.SpaceID)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.LeaderUID = strings.TrimSpace(in.LeaderUID)
	in.Status = strings.TrimSpace(in.Status)
	if in.Status == "" {
		in.Status = string(model.MatterStatusBacklog)
	}
	if userID == "" || in.LetterID == "" || in.SpaceID == "" {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	status := model.MatterStatus(in.Status)
	if status != model.MatterStatusBacklog && status != model.MatterStatusOpen {
		return nil, apperr.InvalidInput(i18n.KeyStatusInvalid)
	}
	if err := s.conversion.verifier.VerifyMatterCreate(ctx, userID, in.SpaceID, callerToken); err != nil {
		return nil, err
	}
	letter, err := s.repo.GetByID(ctx, userID, in.LetterID)
	if err != nil {
		return nil, err
	}
	if convertedID := metadataString(mailboxMetadataMap(letter.Metadata), "converted_matter_id"); convertedID != "" {
		return nil, apperr.Conflict("MAILBOX_ALREADY_CONVERTED", i18n.KeyInvalidRequest)
	}
	desc := strings.TrimSpace("")
	if letter.BodyText != nil {
		desc = strings.TrimSpace(*letter.BodyText)
	}
	if desc == "" && letter.Snippet != nil {
		desc = strings.TrimSpace(*letter.Snippet)
	}
	sourceName := "Mailbox"
	matter := &model.Matter{
		SpaceID:      in.SpaceID,
		Title:        strings.TrimSpace(letter.Title),
		Description:  optionalStringPtr(desc),
		CreatorID:    userID,
		Status:       status,
		SourceName:   &sourceName,
		SourceMsgIDs: model.JSONStringSlice([]string{mailboxLetterSourceID(letter)}),
	}
	if matter.Title == "" {
		matter.Title = "(no subject)"
	}
	if in.ProjectID != "" {
		matter.ProjectID = &in.ProjectID
	}
	if in.LeaderUID != "" {
		matter.LeaderUID = &in.LeaderUID
	}
	assigneeIDs := uniqueNonEmpty(in.AssigneeIDs)
	if s.conversion.v2 != nil {
		existing, err := s.conversion.v2.PrepareCreate(ctx, matter, []string{userID}, callerToken, userID, false, assigneeIDs)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, apperr.Conflict("MAILBOX_CONVERT_DUPLICATE", i18n.KeyInvalidRequest)
		}
	}
	detail, err := s.conversion.creator.CreateMatterWithAssignees(ctx, matter, assigneeIDs)
	if err != nil {
		return nil, err
	}
	if s.conversion.v2 != nil {
		s.conversion.v2.AfterCreate(ctx, matter, userID, assigneeIDs)
	}
	meta := mergeMailboxMetadata(letter.Metadata, map[string]any{
		"converted_matter_id": matter.ID,
		"converted_space_id":  matter.SpaceID,
		"converted_at":        time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err := s.repo.UpdateMetadata(ctx, userID, letter.ID, meta); err != nil {
		return nil, err
	}
	letter.Metadata = meta
	return &MailboxConvertResult{Matter: detail, Letter: letter}, nil
}

func (s *MailboxService) ReplyToLetter(ctx context.Context, userID, letterID, content string) (*MailboxReplyResult, error) {
	if s.repo == nil || s.reply.sender == nil {
		return nil, apperr.FeatureNotConfigured(i18n.KeyInvalidRequest)
	}
	userID = strings.TrimSpace(userID)
	letterID = strings.TrimSpace(letterID)
	content = strings.TrimSpace(content)
	if userID == "" || letterID == "" || content == "" {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	letter, err := s.repo.GetByID(ctx, userID, letterID)
	if err != nil {
		return nil, err
	}
	if letter.SourceType != model.MailboxSourceAgentMail {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	meta := mailboxMetadataMap(letter.Metadata)
	botUID := strings.TrimSpace(metadataString(meta, "bot_uid"))
	mailAddress := strings.ToLower(strings.TrimSpace(metadataString(meta, "mail_address")))
	if botUID == "" || !isValidAgentMailAddress(mailAddress) {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	req := AgentMailReplyRequest{
		UserID:       userID,
		BotUID:       botUID,
		MailAddress:  mailAddress,
		LetterID:     letter.ID,
		SourceRef:    mailboxLetterSourceID(letter),
		ThreadID:     optionalValue(letter.ThreadID),
		RFCMessageID: metadataString(meta, "rfc_message_id"),
		Content:      content,
	}
	sent, err := s.reply.sender.Reply(ctx, req)
	if err != nil {
		return nil, err
	}
	if sent == nil {
		return nil, apperr.Upstream(i18n.KeyUpstream)
	}
	sourceRef := strings.TrimSpace(sent.RFCMessageID)
	if sourceRef == "" {
		sourceRef = strings.TrimSpace(sent.ID)
	}
	if sourceRef == "" {
		return nil, apperr.Upstream(i18n.KeyUpstream)
	}
	threadID := strings.TrimSpace(sent.ThreadID)
	if threadID == "" {
		threadID = req.ThreadID
	}
	title := replyTitle(letter.Title)
	sentAt := time.Now().UTC()
	if sent.SentAt != nil {
		sentAt = *sent.SentAt
	}
	outMeta := mergeMailboxMetadata(nil, map[string]any{
		"reply_to_letter_id":  letter.ID,
		"provider_message_id": strings.TrimSpace(sent.ID),
		"rfc_message_id":      strings.TrimSpace(sent.RFCMessageID),
		"thread_id":           threadID,
		"sent_at":             sentAt.UTC().Format(time.RFC3339Nano),
	})
	outbound := &model.MailboxLetter{
		UserID:     userID,
		SourceType: model.MailboxSourceAgentMail,
		SourceRef:  &sourceRef,
		Direction:  model.MailboxDirectionOutbound,
		ThreadID:   optionalStringPtr(threadID),
		Title:      title,
		BodyText:   optionalStringPtr(content),
		Snippet:    optionalStringPtr(mailboxSnippet(content, 200)),
		FromName:   optionalStringPtr("Me"),
		Metadata:   outMeta,
		CreatedAt:  sentAt,
	}
	if err := s.repo.Upsert(ctx, outbound); err != nil {
		return nil, err
	}
	return &MailboxReplyResult{Letter: outbound}, nil
}

func (s *MailboxService) PushSystemLetterToUser(ctx context.Context, userID, templateID, title, bodyHTML string) error {
	if s.repo == nil {
		return apperr.FeatureNotConfigured(i18n.KeyInvalidRequest)
	}
	userID = strings.TrimSpace(userID)
	templateID = strings.TrimSpace(templateID)
	title = strings.TrimSpace(title)
	if userID == "" || templateID == "" || title == "" {
		return apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	sourceRef := "system:" + templateID
	fromName := "Octo"
	cleanHTML, plainText, snippet := SanitizeEmailHTML(bodyHTML)
	meta, _ := json.Marshal(map[string]string{"template_id": templateID})
	return s.repo.Upsert(ctx, &model.MailboxLetter{
		UserID:     userID,
		SourceType: model.MailboxSourceSystem,
		SourceRef:  &sourceRef,
		Direction:  model.MailboxDirectionInbound,
		Title:      title,
		Snippet:    optionalStringPtr(snippet),
		BodyText:   optionalStringPtr(plainText),
		BodyHTML:   optionalStringPtr(cleanHTML),
		FromName:   &fromName,
		Metadata:   model.MailboxJSON(meta),
	})
}

func mailboxLetterSourceID(letter *model.MailboxLetter) string {
	if letter == nil {
		return ""
	}
	if letter.SourceRef != nil && strings.TrimSpace(*letter.SourceRef) != "" {
		return strings.TrimSpace(*letter.SourceRef)
	}
	return letter.ID
}

func mergeMailboxMetadata(existing model.MailboxJSON, patch map[string]any) model.MailboxJSON {
	out := mailboxMetadataMap(existing)
	for k, v := range patch {
		out[k] = v
	}
	b, _ := json.Marshal(out)
	return model.MailboxJSON(b)
}

func mailboxMetadataMap(existing model.MailboxJSON) map[string]any {
	out := map[string]any{}
	if len(existing) > 0 && json.Valid([]byte(existing)) {
		_ = json.Unmarshal([]byte(existing), &out)
	}
	return out
}

func metadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if s, ok := meta[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func optionalValue(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func replyTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "Re: (no subject)"
	}
	if strings.HasPrefix(strings.ToLower(title), "re:") {
		return title
	}
	return "Re: " + title
}

func (s *MailboxService) PushSystemLetterToUsers(ctx context.Context, userIDs []string, templateID, title, bodyHTML string) error {
	normalized, err := normalizeSystemLetterUserIDs(userIDs)
	if err != nil {
		return err
	}
	for _, userID := range normalized {
		if err := s.PushSystemLetterToUser(ctx, userID, templateID, title, bodyHTML); err != nil {
			return err
		}
	}
	return nil
}

func normalizeSystemLetterUserIDs(userIDs []string) ([]string, error) {
	out := make([]string, 0, len(userIDs))
	seen := map[string]bool{}
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" || len(userID) > 64 {
			return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
		}
		if seen[userID] {
			continue
		}
		seen[userID] = true
		out = append(out, userID)
	}
	if len(out) == 0 {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	return out, nil
}

func stringPtr(s string) *string {
	return &s
}

func optionalStringPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func isValidAgentMailAddress(mailAddress string) bool {
	mailAddress = strings.ToLower(strings.TrimSpace(mailAddress))
	if mailAddress == "" || !strings.HasSuffix(mailAddress, "@agent.qq.com") {
		return false
	}
	parsed, err := mail.ParseAddress(mailAddress)
	return err == nil && strings.EqualFold(strings.TrimSpace(parsed.Address), mailAddress)
}
