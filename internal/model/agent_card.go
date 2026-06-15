package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// AgentCardCapability is one creator-approved ability exposed by an
// AgentCard. Source/status are deliberately explicit so a card can distinguish
// a real OpenClaw skill from a hand-written claim.
type AgentCardCapability struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`     // openclaw | manual | custom; openclaw-* inputs normalize to openclaw
	Status      string `json:"status,omitempty"`     // ready | claimed | needs_setup | disabled | unknown
	Homepage    string `json:"homepage,omitempty"`   // optional registry/source link
	Visibility  string `json:"visibility,omitempty"` // space | owner
}

// AgentCardCapabilities stores structured AgentCard abilities in a nullable
// JSON column while still serializing as [] over the API.
type AgentCardCapabilities []AgentCardCapability

func (c AgentCardCapabilities) MarshalJSON() ([]byte, error) {
	if c == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]AgentCardCapability(c))
}

func (c *AgentCardCapabilities) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*c = nil
		return nil
	}
	var out []AgentCardCapability
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*c = out
	return nil
}

func (c AgentCardCapabilities) Value() (driver.Value, error) {
	if c == nil {
		return nil, nil
	}
	b, err := json.Marshal([]AgentCardCapability(c))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (c *AgentCardCapabilities) Scan(src interface{}) error {
	if src == nil {
		*c = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("AgentCardCapabilities: unsupported scan type")
	}
	if len(raw) == 0 {
		*c = nil
		return nil
	}
	var out []AgentCardCapability
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*c = out
	return nil
}
