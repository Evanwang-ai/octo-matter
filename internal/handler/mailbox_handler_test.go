package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

type fakeInternalAgentMailBindingStore struct {
	items []*model.AgentMailBinding
}

func (f *fakeInternalAgentMailBindingStore) ListByUser(context.Context, string) ([]*model.AgentMailBinding, error) {
	return f.items, nil
}

func (f *fakeInternalAgentMailBindingStore) Upsert(_ context.Context, b *model.AgentMailBinding) error {
	f.items = append(f.items, b)
	return nil
}

func (f *fakeInternalAgentMailBindingStore) Delete(context.Context, string, string) error {
	return nil
}

func TestMailboxRoutesDoNotRequireSpace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewMailboxHandler(nil)
	authMW := func(c *gin.Context) {
		c.Set("uid", "user-1")
		c.Set("role", "user")
		c.Next()
	}
	mailbox := r.Group("/api/v1/mailbox")
	mailbox.Use(authMW, userOnlyMailbox())
	mailbox.GET("/letters", h.List)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mailbox/letters", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected mailbox route without X-Space-Id to pass, got %d", w.Code)
	}
}

func TestMailboxListRejectsInvalidDirection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewMailboxHandler(nil)
	authMW := func(c *gin.Context) {
		c.Set("uid", "user-1")
		c.Set("role", "user")
		c.Next()
	}
	mailbox := r.Group("/api/v1/mailbox")
	mailbox.Use(authMW, userOnlyMailbox())
	mailbox.GET("/letters", h.List)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mailbox/letters?direction=sideways", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid direction to be rejected, got %d", w.Code)
	}
}

func TestMailboxUpdateRejectsInvalidAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		body string
	}{
		{name: "missing action", body: `{}`},
		{name: "bad action", body: `{"action":"restore"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			h := NewMailboxHandler(service.NewMailboxService(nil))
			authMW := func(c *gin.Context) {
				c.Set("uid", "user-1")
				c.Set("role", "user")
				c.Next()
			}
			mailbox := r.Group("/api/v1/mailbox")
			mailbox.Use(authMW, userOnlyMailbox())
			mailbox.PATCH("/letters/:id", h.Update)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/mailbox/letters/letter-1", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected invalid update request to be rejected, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMailboxConvertRejectsInvalidAssigneeIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewMailboxHandler(service.NewMailboxService(nil))
	authMW := func(c *gin.Context) {
		c.Set("uid", "user-1")
		c.Set("role", "user")
		c.Next()
	}
	mailbox := r.Group("/api/v1/mailbox")
	mailbox.Use(authMW, userOnlyMailbox())
	mailbox.POST("/letters/:id/convert", h.Convert)

	w := httptest.NewRecorder()
	body := []byte(`{"space_id":"space-1","assignee_ids":[""]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mailbox/letters/letter-1/convert", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid convert request to be rejected, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMailboxBulkRejectsInvalidRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		body string
	}{
		{name: "empty ids", body: `{"ids":[],"action":"mark_read"}`},
		{name: "bad action", body: `{"ids":["letter-1"],"action":"restore"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			h := NewMailboxHandler(service.NewMailboxService(nil))
			authMW := func(c *gin.Context) {
				c.Set("uid", "user-1")
				c.Set("role", "user")
				c.Next()
			}
			mailbox := r.Group("/api/v1/mailbox")
			mailbox.Use(authMW, userOnlyMailbox())
			mailbox.POST("/letters/bulk", h.Bulk)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/mailbox/letters/bulk", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected invalid bulk request to be rejected, got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMailboxMutationRequiresConfiguredService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewMailboxHandler(nil)
	authMW := func(c *gin.Context) {
		c.Set("uid", "user-1")
		c.Set("role", "user")
		c.Next()
	}
	mailbox := r.Group("/api/v1/mailbox")
	mailbox.Use(authMW, userOnlyMailbox())
	mailbox.GET("/letters/:id", h.Get)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mailbox/letters/letter-1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unconfigured mailbox service to return 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMailboxRoutesRejectBotCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewMailboxHandler(nil)
	authMW := func(c *gin.Context) {
		c.Set("uid", "bot-1")
		c.Set("role", "bot")
		c.Next()
	}
	mailbox := r.Group("/api/v1/mailbox")
	mailbox.Use(authMW, userOnlyMailbox())
	mailbox.GET("/unread-count", h.UnreadCount)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mailbox/unread-count", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected bot caller to be rejected, got %d", w.Code)
	}
}

func TestInternalSystemLetterRejectsInvalidUserIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mailboxSvc := service.NewMailboxService(nil)
	h := NewInternalHandler("secret", nil, nil, nil, nil, nil, mailboxSvc)
	r := gin.New()
	internal := r.Group("/api/v1/internal", h.Auth())
	internal.POST("/mailbox/system-letter", h.PostSystemLetter)

	body := []byte(`{"user_ids":[""],"template_id":"welcome","title":"Welcome","body_html":"<p>hi</p>"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/mailbox/system-letter", bytes.NewReader(body))
	req.Header.Set("X-Internal-Token", "secret")
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid user_ids to be rejected, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestInternalAgentMailActivateStoresOpaqueCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeInternalAgentMailBindingStore{}
	mailboxSvc := service.NewMailboxService(nil, store)
	h := NewInternalHandler("secret", nil, nil, nil, nil, nil, mailboxSvc)
	r := gin.New()
	internal := r.Group("/api/v1/internal", h.Auth())
	internal.POST("/mailbox/agent-mail-bindings/activate", h.ActivateAgentMailBinding)

	body := []byte(`{"user_id":"u1","bot_uid":"bot-a","mail_address":"bot@agent.qq.com","credentials_encrypted_base64":"` +
		base64.StdEncoding.EncodeToString([]byte("encrypted")) + `","sync_cursor":"cursor-1"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/mailbox/agent-mail-bindings/activate", bytes.NewReader(body))
	req.Header.Set("X-Internal-Token", "secret")
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected activate to return 201, got %d body=%s", w.Code, w.Body.String())
	}
	if len(store.items) != 1 {
		t.Fatalf("upsert count = %d, want 1", len(store.items))
	}
	b := store.items[0]
	if b.SyncStatus != model.AgentMailSyncActive || string(b.CredentialsEncrypted) != "encrypted" {
		t.Fatalf("unexpected activated binding: %+v creds=%q", b, string(b.CredentialsEncrypted))
	}
	if b.SyncCursor == nil || *b.SyncCursor != "cursor-1" {
		t.Fatalf("cursor = %v, want cursor-1", b.SyncCursor)
	}
}
