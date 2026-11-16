package oauth

import (
	"context"
	"strings"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
)

// Tokens are opaque, of fosite's HMAC form, with a prefix lodge names its
// own (docs/specs/oauth.md, Names): lgo_at_, lgo_rt_ and lgo_ac_.
var (
	accessTokenPrefix  = "lgo_at_"
	refreshTokenPrefix = "lgo_rt_"
	codePrefix         = "lgo_ac_"
)

// strategy is fosite's HMAC strategy with lodge's prefixes. It is the one
// strategy lodge composes, so it is the core, the access token, the refresh
// token and the authorization code strategy at once.
type strategy struct {
	*oauth2.HMACSHAStrategyUnPrefixed
}

var (
	_ oauth2.CoreStrategy          = (*strategy)(nil)
	_ oauth2.AccessTokenStrategy   = (*strategy)(nil)
	_ oauth2.RefreshTokenStrategy  = (*strategy)(nil)
	_ oauth2.AuthorizeCodeStrategy = (*strategy)(nil)
)

func trimPrefix(token, prefix string) string { return strings.TrimPrefix(token, prefix) }

func setPrefix(token, prefix string) string {
	if token == "" {
		return ""
	}
	return prefix + token
}

// GenerateAccessToken returns a new access token and its signature.
func (h *strategy) GenerateAccessToken(ctx context.Context, r fosite.Requester) (string, string, error) {
	token, sig, err := h.HMACSHAStrategyUnPrefixed.GenerateAccessToken(ctx, r)
	return setPrefix(token, accessTokenPrefix), sig, err
}

// ValidateAccessToken reports whether token is this request's access token.
func (h *strategy) ValidateAccessToken(ctx context.Context, r fosite.Requester, token string) error {
	return h.HMACSHAStrategyUnPrefixed.ValidateAccessToken(ctx, r, trimPrefix(token, accessTokenPrefix))
}

// GenerateRefreshToken returns a new refresh token and its signature.
func (h *strategy) GenerateRefreshToken(ctx context.Context, r fosite.Requester) (string, string, error) {
	token, sig, err := h.HMACSHAStrategyUnPrefixed.GenerateRefreshToken(ctx, r)
	return setPrefix(token, refreshTokenPrefix), sig, err
}

// ValidateRefreshToken reports whether token is this request's refresh
// token.
func (h *strategy) ValidateRefreshToken(ctx context.Context, r fosite.Requester, token string) error {
	return h.HMACSHAStrategyUnPrefixed.ValidateRefreshToken(ctx, r, trimPrefix(token, refreshTokenPrefix))
}

// GenerateAuthorizeCode returns a new authorization code and its signature.
func (h *strategy) GenerateAuthorizeCode(ctx context.Context, r fosite.Requester) (string, string, error) {
	token, sig, err := h.HMACSHAStrategyUnPrefixed.GenerateAuthorizeCode(ctx, r)
	return setPrefix(token, codePrefix), sig, err
}

// ValidateAuthorizeCode reports whether code is this request's code.
func (h *strategy) ValidateAuthorizeCode(ctx context.Context, r fosite.Requester, code string) error {
	return h.HMACSHAStrategyUnPrefixed.ValidateAuthorizeCode(ctx, r, trimPrefix(code, codePrefix))
}
