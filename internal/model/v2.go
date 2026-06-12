package model

import "time"

// MatterProject is the folder-style grouping from the PRD (项目 = 文件夹 + 共享上下文).
type MatterProject struct {
	ID               string    `db:"id" json:"id"`
	SpaceID          string    `db:"space_id" json:"space_id"`
	Name             string    `db:"name" json:"name"`
	Description      *string   `db:"description" json:"description,omitempty"`
	Scope            string    `db:"scope" json:"scope"`
	SourceChannelID  *string   `db:"source_channel_id" json:"source_channel_id,omitempty"`
	SourceName       *string   `db:"source_name" json:"source_name,omitempty"`
	DefaultLeaderUID *string   `db:"default_leader_uid" json:"default_leader_uid,omitempty"`
	CreatorID        string    `db:"creator_id" json:"creator_id"`
	Archived         uint8     `db:"archived" json:"archived"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time `db:"updated_at" json:"updated_at"`
}

// Outbox states. A row is born pending inside the transition transaction,
// turns delivered after the notify POST succeeds, consumed once the target
// uid touches the matter through the API, dead after the retry budget.
const (
	OutboxPending   = "pending"
	OutboxDelivered = "delivered"
	OutboxConsumed  = "consumed"
	OutboxDead      = "dead"
)

// OutboxEventHomecoming rows are NOT personal doorbells: target_uid is the
// SENDER identity (the bot the dispatcher posts as, into the matter's source
// conversation). They must never be parked by the read-consumption hook —
// only the dispatcher closes them after a successful post.
const OutboxEventHomecoming = "matter.homecoming"

// OutboxRow is one doorbell awaiting (or done with) delivery.
type OutboxRow struct {
	ID          string     `db:"id" json:"id"`
	SpaceID     string     `db:"space_id" json:"space_id"`
	MatterID    string     `db:"matter_id" json:"matter_id"`
	TargetUID   string     `db:"target_uid" json:"target_uid"`
	ActorUID    string     `db:"actor_uid" json:"actor_uid"`
	Event       string     `db:"event" json:"event"`
	MessageKey  string     `db:"message_key" json:"message_key"`
	Params      *string    `db:"params" json:"params,omitempty"` // JSON text
	State       string     `db:"state" json:"state"`
	RetryCount  uint       `db:"retry_count" json:"retry_count"`
	NextRetryAt time.Time  `db:"next_retry_at" json:"next_retry_at"`
	LastError   *string    `db:"last_error" json:"last_error,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
}

// MatterFeedback is one 圈一笔 (H taste signal, doc 11).
type MatterFeedback struct {
	ID        string    `db:"id" json:"id"`
	MatterID  string    `db:"matter_id" json:"matter_id"`
	SpaceID   string    `db:"space_id" json:"space_id"`
	AuthorID  string    `db:"author_id" json:"author_id"`
	TargetUID *string   `db:"target_uid" json:"target_uid,omitempty"`
	EntryID   *string   `db:"entry_id" json:"entry_id,omitempty"`
	Anchor    *string   `db:"anchor" json:"-"` // raw JSON; re-marshalled for the wire
	Content   string    `db:"content" json:"content"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Summary lifecycle. There is deliberately no `written` state: OCTO has no
// real interface today that writes preference memory into an agent runtime,
// so rows honestly stop at `authorized` (see gap list).
const (
	SummaryDraft      = "draft"
	SummaryAuthorized = "authorized"
	SummaryDiscarded  = "discarded"
)

// MatterSummary is a Smart-Summary draft / authorization record (T1).
type MatterSummary struct {
	ID           string    `db:"id" json:"id"`
	MatterID     string    `db:"matter_id" json:"matter_id"`
	SpaceID      string    `db:"space_id" json:"space_id"`
	Status       string    `db:"status" json:"status"`
	Content      *string   `db:"content" json:"content,omitempty"`
	TargetBotUID *string   `db:"target_bot_uid" json:"target_bot_uid,omitempty"`
	Scope        *string   `db:"scope" json:"scope,omitempty"`
	CreatedBy    string    `db:"created_by" json:"created_by"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`
}

// MatterSchedule is a 常设委托单 (Or5): cron-shaped standing delegation that
// stamps out matters idempotently.
type MatterSchedule struct {
	ID          string     `db:"id" json:"id"`
	SpaceID     string     `db:"space_id" json:"space_id"`
	Title       string     `db:"title" json:"title"`
	Runbook     *string    `db:"runbook" json:"runbook,omitempty"`
	CronExpr    string     `db:"cron_expr" json:"cron_expr"`
	Timezone    string     `db:"timezone" json:"timezone"`
	ExecutorUID string     `db:"executor_uid" json:"executor_uid"`
	// OutputMode: track=每次运行立成事项追踪; runonly=结果发回会话(投递由
	// 执行 agent 完成 — O3 report-back;matter 只在门铃里携带目标)。
	OutputMode        string  `db:"output_mode" json:"output_mode"`
	TargetChannelID   *string `db:"target_channel_id" json:"target_channel_id,omitempty"`
	TargetChannelName *string `db:"target_channel_name" json:"target_channel_name,omitempty"`
	ProjectID   *string    `db:"project_id" json:"project_id,omitempty"`
	CreatorID   string     `db:"creator_id" json:"creator_id"`
	Enabled     uint8      `db:"enabled" json:"enabled"`
	LastRunAt   *time.Time `db:"last_run_at" json:"last_run_at,omitempty"`
	NextRunAt   *time.Time `db:"next_run_at" json:"next_run_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
}

// MatterProjectSource is one 共享上下文来源 on a project (H-mounted: a chat
// excerpt, file pointer or link — doc 01 来源纪律, PRD 项目双 tab).
type MatterProjectSource struct {
	ID        string    `db:"id" json:"id"`
	ProjectID string    `db:"project_id" json:"project_id"`
	SpaceID   string    `db:"space_id" json:"space_id"`
	Kind      string    `db:"kind" json:"kind"`
	Title     string    `db:"title" json:"title"`
	Ref       *string   `db:"ref" json:"ref,omitempty"`
	Snippet   *string   `db:"snippet" json:"snippet,omitempty"`
	CreatedBy string    `db:"created_by" json:"created_by"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Bot-task states mirror octo-fleet's bot_task (queued → dispatched →
// succeeded|failed).
const (
	BotTaskQueued     = "queued"
	BotTaskDispatched = "dispatched"
	BotTaskSucceeded  = "succeeded"
	BotTaskFailed     = "failed"
)

// MatterBotTask is one queued agent run (the queue PR-B.3 moved here from fleet).
type MatterBotTask struct {
	ID            int64     `db:"id" json:"id"`
	MatterID      string    `db:"matter_id" json:"matter_id"`
	SpaceID       string    `db:"space_id" json:"space_id"`
	BotUID        string    `db:"bot_uid" json:"bot_uid"`
	RequesterUID  string    `db:"requester_uid" json:"requester_uid"`
	Title         string    `db:"title" json:"title"`
	Description   *string   `db:"description" json:"description,omitempty"`
	Prompt        *string   `db:"prompt" json:"prompt,omitempty"`
	Status        string    `db:"status" json:"status"`
	ClaimToken    *string   `db:"claim_token" json:"-"`
	ClaimedBy     *string   `db:"claimed_by" json:"claimed_by,omitempty"`
	ResultSummary *string   `db:"result_summary" json:"result_summary,omitempty"`
	ErrorMsg      *string   `db:"error_msg" json:"error_msg,omitempty"`
	CreatedBy     string    `db:"created_by" json:"created_by"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
}

// MatterAgentCard is the DECLARED half of an AgentCard (doc 04 §五):
// creator-curated identity — what this bot is for, which skills it offers,
// which external systems it can reach. The earned half (acceptance stats,
// authorized preference summaries) is derived live and never stored here.
type MatterAgentCard struct {
	BotUID      string          `db:"bot_uid" json:"bot_uid"`
	SpaceID     string          `db:"space_id" json:"space_id"`
	OwnerUID    string          `db:"owner_uid" json:"owner_uid"`
	Tagline     *string         `db:"tagline" json:"tagline,omitempty"`
	Description *string         `db:"description" json:"description,omitempty"`
	Skills      JSONStringSlice `db:"skills" json:"skills"`
	Systems     JSONStringSlice `db:"systems" json:"systems"`
	// Visibility: space(默认,全空间可见) | private(声明半仅主人可见)。
	Visibility string    `db:"visibility" json:"visibility"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}
