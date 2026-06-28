package model

import "time"

const (
	AgentMailSyncActive = "active"
	AgentMailSyncPaused = "paused"
	AgentMailSyncError  = "error"
)

func IsValidAgentMailSyncStatus(s string) bool {
	switch s {
	case "", AgentMailSyncActive, AgentMailSyncPaused, AgentMailSyncError:
		return true
	}
	return false
}

type AgentMailBinding struct {
	ID                   string     `db:"id" json:"id"`
	UserID               string     `db:"user_id" json:"user_id"`
	BotUID               string     `db:"bot_uid" json:"bot_uid"`
	MailAddress          string     `db:"mail_address" json:"mail_address"`
	CredentialsEncrypted []byte     `db:"credentials_encrypted" json:"-"`
	SyncCursor           *string    `db:"sync_cursor" json:"sync_cursor,omitempty"`
	SyncStatus           string     `db:"sync_status" json:"sync_status"`
	LastSyncAt           *time.Time `db:"last_sync_at" json:"last_sync_at,omitempty"`
	LastError            *string    `db:"last_error" json:"last_error,omitempty"`
	RetryCount           int        `db:"retry_count" json:"retry_count"`
	DeletedAt            *time.Time `db:"deleted_at" json:"deleted_at,omitempty"`
	CreatedAt            time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at" json:"updated_at"`
}
