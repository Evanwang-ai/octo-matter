package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// SpaceMiddleware must classify the upstream space-verify verdict honestly:
// a 4xx (not a member / space gone) is a permanent 403, NOT a transient 503.
// A forged or stale X-Space-Id used to surface as "服务不可用", inviting a
// pointless retry. 5xx / network failures stay 503.
func TestSpaceMiddleware_UpstreamStatusClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name           string
		upstreamStatus int
		wantStatus     int
	}{
		{"member → pass through", http.StatusOK, http.StatusOK},
		{"not a member → 403", http.StatusForbidden, http.StatusForbidden},
		{"space not found → 403", http.StatusNotFound, http.StatusForbidden},
		{"unauthorized → 403", http.StatusUnauthorized, http.StatusForbidden},
		{"upstream 500 → 503", http.StatusInternalServerError, http.StatusServiceUnavailable},
		{"upstream 502 → 503", http.StatusBadGateway, http.StatusServiceUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.upstreamStatus)
			}))
			defer upstream.Close()

			r := gin.New()
			r.Use(SpaceMiddleware(upstream.URL))
			r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("X-Space-Id", "space-123")
			req.Header.Set("token", "tok-abcdefghijklmnop")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("upstream %d → got %d, want %d", tc.upstreamStatus, w.Code, tc.wantStatus)
			}
		})
	}
}

func TestSpaceMiddleware_MissingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SpaceMiddleware("http://unused.invalid"))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("token", "tok-abcdefghijklmnop")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing X-Space-Id → got %d, want 400", w.Code)
	}
}
