// Package i18n is octo-matter's lightweight localization layer. It wraps
// go-i18n with an embedded TOML message catalog (zh-CN / en-US), HTTP language
// negotiation aligned with octo-server (X-Octo-Lang > ?lang > cookie >
// user.language > Accept-Language > default), and a Gin-aware error responder.
//
// Message IDs are stable keys under err.* / notify.* segments. en-US is the
// source language; zh-CN is the translation. Missing translations fall back to
// the source string, then to the key itself.
package i18n

// Error message keys. Grouped by segment; the string value IS the go-i18n
// MessageID and must match a [section] header in locales/active.*.toml.
const (
	// Generic / transport
	KeyNotFound        = "err.common.not_found"
	KeyForbidden       = "err.common.forbidden"
	KeyInternal        = "err.common.internal"
	KeyPayloadTooLarge = "err.common.payload_too_large"

	// Auth + space (auth/space middleware)
	KeyAuthMissingToken    = "err.auth.missing_token"
	KeyAuthUnavailable     = "err.auth.unavailable"
	KeyAuthInvalidToken    = "err.auth.invalid_token"
	KeyAuthInvalidBotToken = "err.auth.invalid_bot_token"
	KeyAuthParseFailed     = "err.auth.parse_failed"
	KeySpaceMissingHeader  = "err.space.missing_header"
	KeySpaceForbidden      = "err.space.forbidden"
	KeySpaceUnavailable    = "err.space.unavailable"

	// Validation
	KeyInvalidID           = "err.validation.invalid_id"
	KeyInvalidEntryID      = "err.validation.invalid_entry_id"
	KeyInvalidCursor       = "err.validation.invalid_cursor"
	KeyInvalidRequest      = "err.validation.invalid_request"
	KeyMatterIDMismatch    = "err.validation.matter_id_mismatch"
	KeyContentRequired     = "err.validation.content_required"
	KeyContentTooLong      = "err.validation.content_too_long"
	KeyTooManyAttachments  = "err.validation.too_many_attachments"
	KeyFileURLRequired     = "err.validation.file_url_required"
	KeyAttachmentTooLarge  = "err.validation.attachment_too_large"
	KeyParticipantUIDReq   = "err.validation.participant_uid_required"
	KeyChannelIDRequired   = "err.validation.channel_id_required"
	KeyChannelTypeInvalid  = "err.validation.channel_type_invalid"
	KeyCreatorUIDRequired  = "err.validation.creator_uid_required"
	KeyMsgsRequired        = "err.validation.msgs_required"
	KeyMsgsLimit           = "err.validation.msgs_limit"       // params: Limit
	KeyMessageIDLimit      = "err.validation.message_id_limit" // params: Index, Limit
	KeyDeadlineFormat      = "err.validation.deadline_format"
	KeyRemindAtFormat      = "err.validation.remind_at_format"
	KeyStatusInvalid       = "err.validation.status_invalid"
	KeyAssigneeUIDRequired = "err.validation.assignee_uid_required"
	KeyCallerIdentityReq   = "err.validation.caller_identity_required"
	KeyTransitionArchived  = "err.validation.transition_archived"

	// Forbidden
	KeyMatterAccess              = "err.forbidden.matter_access"
	KeyMatterView                = "err.forbidden.matter_view"
	KeyParticipantNotAuthorized  = "err.forbidden.participant_not_authorized"
	KeyCreatorUIDNotAuthorized   = "err.forbidden.creator_uid_not_authorized"
	KeyBotLinkChannel            = "err.forbidden.bot_link_channel"
	KeyBotLinkExisting           = "err.forbidden.bot_link_existing"
	KeyNotChannelMember          = "err.forbidden.not_channel_member"
	KeyOnlyCreatorArchive        = "err.forbidden.only_creator_archive"
	KeyOnlyCreatorOrAssigneeStat = "err.forbidden.only_creator_or_assignee_status"

	// Not found
	KeyMatterNotFound   = "err.notfound.matter"
	KeyAssigneeNotFound = "err.notfound.assignee"

	// Upstream / rate limit / conflict
	KeyUpstream          = "err.upstream.generic"
	KeyChannelMembership = "err.upstream.channel_membership"
	KeyRateLimited       = "err.ratelimited.generic"
	KeyRateLimitCooldown = "err.ratelimited.cooldown" // params: Cooldown (integer seconds)
	KeyDuplicateAssignee = "err.conflict.duplicate_assignee"

	// LLM
	KeyLLMEmptyExtraction = "err.llm.empty_extraction"
	KeyLLMUpstream        = "err.llm.upstream"
)

// Notification message keys (rendered for the default-language fallback string;
// IM localizes per-recipient from the same key + params).
const (
	KeyNotifyMatterCreated      = "notify.matter_created"       // params: Title, Actor
	KeyNotifyStatusChanged      = "notify.status_changed"       // params: Title, Actor, Action
	KeyNotifyAssigneeAdded      = "notify.assignee_added"       // params: Title, Actor
	KeyNotifyTimelineEntryAdded = "notify.timeline_entry_added" // params: Title, Actor

	KeyNotifyActionDone     = "notify.action.done"
	KeyNotifyActionArchived = "notify.action.archived"
	KeyNotifyActionOpen     = "notify.action.open"
	KeyNotifyActionUpdated  = "notify.action.updated"
)

// Matter v2 keys: six-state guard errors and doorbell notifications.
const (
	// Guard / engine errors
	KeyVersionConflict         = "err.matter.version_conflict"
	KeyEpochStale              = "err.matter.epoch_stale"
	KeyChildrenNotTerminal     = "err.matter.children_not_terminal"
	KeyBlockReasonRequired     = "err.matter.block_reason_required"
	KeyNoSelfAcceptance        = "err.matter.no_self_acceptance"
	KeyOnlyAcceptanceAuthority = "err.matter.only_acceptance_authority"
	KeyTransitionNotAllowed    = "err.matter.transition_not_allowed"
	KeyParentNotFound          = "err.matter.parent_not_found"
	KeyModeInvalid             = "err.matter.mode_invalid"
	KeyExecutorNotOwnBot       = "err.matter.executor_not_own_bot"
	KeyCronInvalid             = "err.matter.cron_invalid"
	KeyFeedbackUsersOnly       = "err.matter.feedback_users_only"
	KeyLLMNotConfigured        = "err.llm.not_configured"
	KeySummaryOnlyCreator      = "err.matter.summary_only_creator"

	// Doorbells (params: Title, Seq, Actor, Edge, Reason)
	KeyDoorbellAssigned        = "notify.doorbell.assigned"
	KeyDoorbellHandedBack      = "notify.doorbell.handed_back"
	KeyDoorbellChildHandedBack = "notify.doorbell.child_handed_back"
	KeyDoorbellNextSegment     = "notify.doorbell.next_segment"
	KeyDoorbellVerify          = "notify.doorbell.verify"
	KeyDoorbellSentBack        = "notify.doorbell.sent_back"
	KeyDoorbellFeedback        = "notify.doorbell.feedback"
	KeyDoorbellBlocked         = "notify.doorbell.blocked"
	KeyDoorbellCancelled       = "notify.doorbell.cancelled"
	KeyDoorbellReassigned      = "notify.doorbell.reassigned"
	KeyDoorbellDone            = "notify.doorbell.done"
	KeyDoorbellReflect         = "notify.doorbell.reflect"
	KeyDoorbellRevive          = "notify.doorbell.revive"
	KeyDoorbellWatchdogBlocked = "notify.doorbell.watchdog_blocked"
	KeyDoorbellSchedule        = "notify.doorbell.schedule"
	KeyDoorbellContextAdded    = "notify.doorbell.context_added"
	KeyDoorbellProjectDead     = "notify.doorbell.project_dead"

	// Manual send-back validation
	KeySendBackNoSource = "err.matter.sendback_no_source"
	KeySendBackNoBot    = "err.matter.sendback_no_bot"

	// Preference draft (护栏4)
	KeySummaryOnlyLeaderBot    = "err.matter.summary_only_leader_bot"
	KeyDoorbellSummaryDraft    = "notify.doorbell.summary_draft"
	KeyDoorbellSummaryApproved = "notify.doorbell.summary_approved"
	KeyDoorbellSummaryRejected = "notify.doorbell.summary_rejected"
)
