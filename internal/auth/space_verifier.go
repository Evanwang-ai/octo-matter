package auth

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
)

type SpaceCreateVerifier struct {
	baseURL string
	client  *http.Client
}

func NewSpaceCreateVerifier(octoIMURL string) *SpaceCreateVerifier {
	return &SpaceCreateVerifier{
		baseURL: strings.TrimRight(octoIMURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (v *SpaceCreateVerifier) VerifyMatterCreate(ctx context.Context, userID, spaceID, token string) error {
	if v == nil || strings.TrimSpace(v.baseURL) == "" {
		return apperr.FeatureNotConfigured(i18n.KeyInvalidRequest)
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(spaceID) == "" || strings.TrimSpace(token) == "" {
		return apperr.Forbidden(i18n.KeySpaceForbidden)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+"/v1/space/"+spaceID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("token", token)
	resp, err := v.client.Do(req)
	if err != nil {
		log.Printf("SpaceCreateVerifier: octoim space check failed: %v", err)
		return apperr.Upstream(i18n.KeySpaceUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return apperr.SpaceForbidden()
	}
	log.Printf("SpaceCreateVerifier: octoim space check returned status %d", resp.StatusCode)
	return apperr.Upstream(i18n.KeySpaceUnavailable)
}

func (v *SpaceCreateVerifier) String() string {
	if v == nil || v.baseURL == "" {
		return "SpaceCreateVerifier(<nil>)"
	}
	return fmt.Sprintf("SpaceCreateVerifier(%s)", v.baseURL)
}
