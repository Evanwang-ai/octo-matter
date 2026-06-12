package notification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ChannelSender posts a message into an IM conversation AS a bot — the
// homecoming leg (审核中/受阻 → 自动发回来源会话). Backed by octo-server's
// internal endpoint; the bot must genuinely be a member of the channel,
// octo-server re-checks.
type ChannelSender interface {
	SendChannelMessage(fromUID, channelID string, channelType uint8, content string, mentionUIDs []string) error
}

// BotGroup is one conversation a bot is a member of — picker data for the
// automation "send result to" target (no human ever types a channel id).
type BotGroup struct {
	GroupNo string `json:"group_no"`
	Name    string `json:"name"`
	SpaceID string `json:"space_id,omitempty"`
}

// BotGroupLister is implemented by OctoNotifier; handlers type-assert so the
// Noop notifier (notifications off) degrades to "picker unavailable".
type BotGroupLister interface {
	ListBotGroups(botUID string) ([]BotGroup, error)
}

// ListBotGroups proxies octo-server GET /v1/internal/bot/groups.
func (n *OctoNotifier) ListBotGroups(botUID string) ([]BotGroup, error) {
	req, err := http.NewRequest(http.MethodGet, n.baseURL+"/v1/internal/bot/groups?bot_uid="+url.QueryEscape(botUID), nil)
	if err != nil {
		return nil, err
	}
	if n.token != "" {
		req.Header.Set("X-Internal-Token", n.token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("bot groups returned %d", resp.StatusCode)
	}
	var groups []BotGroup
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// SendChannelMessage posts via octo-server POST /v1/internal/bot/sendMessage
// (X-Internal-Token). Synchronous like SendDoorbell: the outbox dispatcher
// owns retries, so failures must surface.
func (n *OctoNotifier) SendChannelMessage(fromUID, channelID string, channelType uint8, content string, mentionUIDs []string) error {
	if fromUID == "" || channelID == "" {
		return nil
	}
	payload := map[string]interface{}{
		"type":    1,
		"content": content,
		"message": content,
	}
	if len(mentionUIDs) > 0 {
		payload["mention"] = map[string]interface{}{"uids": mentionUIDs}
	}
	body, err := json.Marshal(map[string]interface{}{
		"channel_id":   channelID,
		"channel_type": channelType,
		"from_uid":     fromUID,
		"payload":      payload,
	})
	if err != nil {
		return fmt.Errorf("marshal channel message: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, n.baseURL+"/v1/internal/bot/sendMessage", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build channel message request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		req.Header.Set("X-Internal-Token", n.token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("channel message POST: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("channel message returned %d", resp.StatusCode)
	}
	return nil
}
