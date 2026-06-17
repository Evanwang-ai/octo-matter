package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

type PreferenceCard struct {
	ID        string    `db:"id" json:"id"`
	SpaceID   string    `db:"space_id" json:"space_id"`
	MatterID  *string   `db:"matter_id" json:"matter_id,omitempty"`
	ProjectID *string   `db:"project_id" json:"project_id,omitempty"`
	AgentUID  *string   `db:"agent_uid" json:"agent_uid,omitempty"`
	CreatorID string    `db:"creator_id" json:"creator_id"`
	Status    string    `db:"status" json:"status"`
	Scope     string    `db:"scope" json:"scope"`
	Content   string    `db:"content" json:"content"`
	Evidence  *string   `db:"evidence" json:"evidence,omitempty"`
	Avoid     *string   `db:"avoid" json:"avoid,omitempty"`
	Keywords  CardJSON  `db:"keywords" json:"keywords"`
	Links     CardJSON  `db:"links" json:"links"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

type CardJSON json.RawMessage

func (c CardJSON) MarshalJSON() ([]byte, error) {
	if c == nil || len(c) == 0 {
		return []byte("[]"), nil
	}
	if !json.Valid([]byte(c)) {
		return []byte("[]"), nil
	}
	return []byte(c), nil
}

func (c *CardJSON) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*c = nil
		return nil
	}
	*c = CardJSON(data)
	return nil
}

func (c CardJSON) Value() (driver.Value, error) {
	if c == nil {
		return nil, nil
	}
	return string(c), nil
}

func (c *CardJSON) Scan(src interface{}) error {
	if src == nil {
		*c = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*c = CardJSON(v)
	case string:
		*c = CardJSON(v)
	default:
		return errors.New("CardJSON: unsupported scan type")
	}
	return nil
}
