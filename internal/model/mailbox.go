package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

type MailboxSourceType string
type MailboxDirection string

const (
	MailboxSourceSystem    MailboxSourceType = "system"
	MailboxSourceAgentMail MailboxSourceType = "agent_mail"

	MailboxDirectionInbound  MailboxDirection = "inbound"
	MailboxDirectionOutbound MailboxDirection = "outbound"
)

func IsValidMailboxSourceType(s MailboxSourceType) bool {
	switch s {
	case "", MailboxSourceSystem, MailboxSourceAgentMail:
		return true
	}
	return false
}

func IsValidMailboxDirection(s MailboxDirection) bool {
	switch s {
	case "", MailboxDirectionInbound, MailboxDirectionOutbound:
		return true
	}
	return false
}

type MailboxLetter struct {
	ID         string            `db:"id" json:"id"`
	UserID     string            `db:"user_id" json:"user_id"`
	SourceType MailboxSourceType `db:"source_type" json:"source_type"`
	SourceRef  *string           `db:"source_ref" json:"source_ref,omitempty"`
	Direction  MailboxDirection  `db:"direction" json:"direction"`
	ThreadID   *string           `db:"thread_id" json:"thread_id,omitempty"`
	Title      string            `db:"title" json:"title"`
	Snippet    *string           `db:"snippet" json:"snippet,omitempty"`
	BodyText   *string           `db:"body_text" json:"body_text,omitempty"`
	BodyHTML   *string           `db:"body_html" json:"body_html,omitempty"`
	FromName   *string           `db:"from_name" json:"from_name,omitempty"`
	FromEmail  *string           `db:"from_email" json:"from_email,omitempty"`
	Metadata   MailboxJSON       `db:"metadata" json:"metadata,omitempty"`
	ReadAt     *time.Time        `db:"read_at" json:"read_at,omitempty"`
	ArchivedAt *time.Time        `db:"archived_at" json:"archived_at,omitempty"`
	DeletedAt  *time.Time        `db:"deleted_at" json:"deleted_at,omitempty"`
	CreatedAt  time.Time         `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time         `db:"updated_at" json:"updated_at"`
}

type MailboxJSON json.RawMessage

func (m MailboxJSON) MarshalJSON() ([]byte, error) {
	if m == nil || len(m) == 0 {
		return []byte("{}"), nil
	}
	if !json.Valid([]byte(m)) {
		return []byte("{}"), nil
	}
	return []byte(m), nil
}

func (m *MailboxJSON) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*m = nil
		return nil
	}
	*m = MailboxJSON(data)
	return nil
}

func (m MailboxJSON) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return string(m), nil
}

func (m *MailboxJSON) Scan(src interface{}) error {
	if src == nil {
		*m = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*m = MailboxJSON(v)
	case string:
		*m = MailboxJSON(v)
	default:
		return errors.New("MailboxJSON: unsupported scan type")
	}
	return nil
}
