package notification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
)

// DoorbellSender delivers one outbox row to one target. Unlike the
// fire-and-forget Notifier methods this is synchronous and returns the error:
// the outbox dispatcher owns retries/backoff, so failures must surface.
type DoorbellSender interface {
	SendDoorbell(spaceID, event, actorUID, targetUID, messageKey string, params map[string]any) error
}

// SendDoorbell posts a single-target notify through octo-server
// /v1/internal/notify. Delivery beyond that point (WuKongIM push) is the IM
// server's job; a 2xx from octo-server counts as delivered.
//
// NOTE (gap list): the design wants an in-channel @-mention via the adapter;
// octo-server exposes no internal send-message-as-bot API today, so the
// doorbell lands as a personal notification instead.
func (n *OctoNotifier) SendDoorbell(spaceID, event, actorUID, targetUID, messageKey string, params map[string]any) error {
	if spaceID == "" || targetUID == "" {
		return nil
	}
	// The notify payload is forwarded verbatim as the WuKongIM message body;
	// clients dispatch rendering on payload.type. type=1 + content is the
	// plain-text shape every deployed client renders (octo-server's own
	// notify tests use exactly {"type":1,"content":...}); without it the web
	// client falls back to “此消息不支持查看”.
	text := i18n.Localize(n.defaultLang, messageKey, params)
	// Channel adapters hand agents only the rendered text and drop the
	// structured payload keys, so bot targets need the matter UUID (and how
	// to fetch it) inside the text itself. Human notifications stay clean.
	if id, ok := params["matter_id"]; ok && strings.HasSuffix(targetUID, "_bot") {
		text = fmt.Sprintf("%s\nmatter_id=%v\n读单: octo-cli api GET /api/v1/matters/%v", text, id, id)
	}
	payload := map[string]interface{}{
		"type":        1,
		"content":     text,
		"message_key": messageKey,
		"params":      params,
		"message":     text,
	}
	if v, ok := params["matter_id"]; ok {
		payload["matter_id"] = v
	}
	if v, ok := params["seq_no"]; ok {
		payload["seq_no"] = v
	}
	if v, ok := params["Edge"]; ok {
		payload["edge"] = v
	}
	if v, ok := params["events_seq"]; ok {
		payload["events_seq"] = v
	}
	if v, ok := params["epoch"]; ok {
		payload["epoch"] = v
	}
	body, err := json.Marshal(notifyRequest{
		SpaceID:  spaceID,
		Service:  notifyService,
		Event:    event,
		Targets:  []string{targetUID},
		ActorUID: actorUID,
		Payload:  payload,
	})
	if err != nil {
		return fmt.Errorf("marshal doorbell: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, n.baseURL+"/v1/internal/notify", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build doorbell request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		req.Header.Set("X-Internal-Token", n.token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("doorbell POST: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("doorbell notify returned %d", resp.StatusCode)
	}
	return nil
}

// NoopDoorbell satisfies DoorbellSender for tests / notifications-off mode.
type NoopDoorbell struct{}

func (NoopDoorbell) SendDoorbell(_, _, _, _, _ string, _ map[string]any) error { return nil }
