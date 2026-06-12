package model

import "time"

// MatterStatus is the six-state machine from the v2 design (doc 02.5) plus
// the legacy v1 value `archived` kept so pre-v2 rows and clients survive.
type MatterStatus string

const (
	MatterStatusOpen       MatterStatus = "open"        // 待办
	MatterStatusInProgress MatterStatus = "in_progress" // 进行中
	MatterStatusReview     MatterStatus = "review"      // 审核中 (handed back, awaiting human judgement)
	MatterStatusDone       MatterStatus = "done"        // 完成 (acceptance — 品鉴权)
	MatterStatusBlocked    MatterStatus = "blocked"     // 受阻 (reason kind: agent|system)
	MatterStatusCancelled  MatterStatus = "cancelled"   // 取消 (terminal, off-board)
	MatterStatusArchived   MatterStatus = "archived"    // legacy v1, creator-only
)

// IsValidStatus reports whether s is a known MatterStatus.
func IsValidStatus(s MatterStatus) bool {
	switch s {
	case MatterStatusOpen, MatterStatusInProgress, MatterStatusReview,
		MatterStatusDone, MatterStatusBlocked, MatterStatusCancelled,
		MatterStatusArchived:
		return true
	}
	return false
}

// IsTerminalStatus reports whether s ends the matter's lifecycle.
// Used by the parent→done guard ("非取消子全部终态才允许").
func IsTerminalStatus(s MatterStatus) bool {
	return s == MatterStatusDone || s == MatterStatusCancelled || s == MatterStatusArchived
}

// Collaboration modes (doc 04 §五种信息传递机制), stored on the parent matter.
const (
	ModeSolo       = "solo"
	ModeSplit      = "split"      // 分头干 — partition, children mutually blind
	ModeSwarm      = "swarm"      // 撒网 — replicate, children MUST be blind
	ModeRoundtable = "roundtable" // 圆桌 — blackboard, all children see all
	ModePipeline   = "pipeline"   // 流水线 — chain, segment k rings segment k+1
	ModeCritic     = "critic"     // 生成-验证 — loop pair, verifier holds veto
)

// IsValidMode reports whether m is a known collaboration mode ("" = unset).
func IsValidMode(m string) bool {
	switch m {
	case "", ModeSolo, ModeSplit, ModeSwarm, ModeRoundtable, ModePipeline, ModeCritic:
		return true
	}
	return false
}

// Block-reason producers (受阻双措辞, doc 02.5).
const (
	BlockKindAgent  = "agent"  // 负责 agent 报缺 — “卡住了:缺 X”
	BlockKindSystem = "system" // watchdog — “已经 N 没动静”
)

// Matter represents the atomic delegation unit.
type Matter struct {
	ID                string       `db:"id" json:"id"`
	SeqNo             int          `db:"seq_no" json:"seq_no"`
	SpaceID           string       `db:"space_id" json:"space_id"`
	ParentMatterID    *string      `db:"parent_matter_id" json:"parent_matter_id,omitempty"`
	Title             string       `db:"title" json:"title"`
	Description       *string      `db:"description" json:"description,omitempty"`
	// Brief 折叠字段 (doc 02 / 05: 硬约束+验收 折进 Brief; 来源 H,人挂载)
	BriefConstraints *string `db:"brief_constraints" json:"brief_constraints,omitempty"`
	BriefOutputSpec  *string `db:"brief_output_spec" json:"brief_output_spec,omitempty"`
	CreatorID         string       `db:"creator_id" json:"creator_id"`
	LeaderUID         *string      `db:"leader_uid" json:"leader_uid,omitempty"`
	Status            MatterStatus `db:"status" json:"status"`
	Mode              *string      `db:"mode" json:"mode,omitempty"`
	StepID            *string      `db:"step_id" json:"step_id,omitempty"`
	StepOrder         *uint        `db:"step_order" json:"step_order,omitempty"`
	ProjectID         *string      `db:"project_id" json:"project_id,omitempty"`
	AssignmentEpoch   uint         `db:"assignment_epoch" json:"assignment_epoch"`
	Version           int64        `db:"version" json:"version"`
	EventsSeq         int64        `db:"events_seq" json:"events_seq"`
	ProcessedSeq      int64        `db:"processed_seq" json:"processed_seq"`
	Inflight          uint8        `db:"inflight" json:"inflight"`
	ExpectedDuration  *uint        `db:"expected_duration_minutes" json:"expected_duration_minutes,omitempty"`
	LastActivityAt    *time.Time   `db:"last_activity_at" json:"last_activity_at,omitempty"`
	LastTransitionAt  *time.Time   `db:"last_transition_at" json:"last_transition_at,omitempty"`
	LastWatchdogAlert *time.Time   `db:"last_watchdog_alert_at" json:"-"`
	BlockReasonKind   *string      `db:"block_reason_kind" json:"block_reason_kind,omitempty"`
	BlockReasonText   *string      `db:"block_reason_text" json:"block_reason_text,omitempty"`
	ScheduleID        *string      `db:"schedule_id" json:"schedule_id,omitempty"`
	ScheduledAt       *time.Time   `db:"scheduled_at" json:"scheduled_at,omitempty"`
	Deadline          *time.Time   `db:"deadline" json:"deadline,omitempty"`
	RemindAt          *time.Time   `db:"remind_at" json:"remind_at,omitempty"`
	SourceChannelID   *string      `db:"source_channel_id" json:"source_channel_id,omitempty"`
	SourceChannelType *uint8       `db:"source_channel_type" json:"source_channel_type,omitempty"`
	SourceName        *string      `db:"source_name" json:"source_name,omitempty"`
	// JSON tag matches TimelineEntry.SourceMsgs so matter and timeline
	// responses use one wire name. DB column keeps its more explicit
	// `source_msg_ids` (no migration needed). Always emitted (even as []) so
	// clients can rely on the field being present: NULL rows and explicit
	// empty rows both render `[]`.
	SourceMsgIDs JSONStringSlice `db:"source_msg_ids" json:"source_msgs"`
	// Derived by list queries only (EXISTS over children); zero elsewhere.
	// Lets list rows show the expander only where expanding does anything.
	HasChildren bool       `db:"has_children" json:"has_children"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
	DeletedAt   *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
}

// LeaderOrEmpty returns the leader uid or "".
func (m *Matter) LeaderOrEmpty() string {
	if m.LeaderUID == nil {
		return ""
	}
	return *m.LeaderUID
}
