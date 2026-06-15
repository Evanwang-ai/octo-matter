package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

type InputAttachment struct {
	FileURL  string  `json:"file_url"`
	FileName string  `json:"file_name"`
	FileSize int64   `json:"file_size,omitempty"`
	MimeType *string `json:"mime_type,omitempty"`
}

type InputAttachments []InputAttachment

func (a InputAttachments) MarshalJSON() ([]byte, error) {
	if a == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]InputAttachment(a))
}

func (a *InputAttachments) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*a = nil
		return nil
	}
	var out []InputAttachment
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*a = out
	return nil
}

func (a InputAttachments) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	b, err := json.Marshal([]InputAttachment(a))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (a *InputAttachments) Scan(src interface{}) error {
	if src == nil {
		*a = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("InputAttachments: unsupported scan type")
	}
	if len(raw) == 0 {
		*a = nil
		return nil
	}
	var out []InputAttachment
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*a = out
	return nil
}
