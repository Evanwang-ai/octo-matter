package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
)

func TestSpaceCreateVerifierAllowsMember(t *testing.T) {
	var gotToken string
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("token")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	v := NewSpaceCreateVerifier(srv.URL)
	if err := v.VerifyMatterCreate(context.Background(), "u1", "space-1", "token-1"); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if gotToken != "token-1" || gotPath != "/v1/space/space-1" {
		t.Fatalf("unexpected request token=%q path=%q", gotToken, gotPath)
	}
}

func TestSpaceCreateVerifierMapsForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	v := NewSpaceCreateVerifier(srv.URL)
	err := v.VerifyMatterCreate(context.Background(), "u1", "space-1", "token-1")
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "SPACE_FORBIDDEN" {
		t.Fatalf("err = %v, want SPACE_FORBIDDEN", err)
	}
}

func TestSpaceCreateVerifierMapsUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	v := NewSpaceCreateVerifier(srv.URL)
	err := v.VerifyMatterCreate(context.Background(), "u1", "space-1", "token-1")
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("err = %v, want UPSTREAM_UNAVAILABLE", err)
	}
}

func TestSpaceCreateVerifierFailsClosedWithoutToken(t *testing.T) {
	v := NewSpaceCreateVerifier("http://127.0.0.1:1")
	err := v.VerifyMatterCreate(context.Background(), "u1", "space-1", "")
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "FORBIDDEN" {
		t.Fatalf("err = %v, want FORBIDDEN", err)
	}
}
