package teams

import (
	"context"
	"fmt"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// Bot Framework inbound-token constants. Mirrors the Microsoft 365 Agents SDK
// (jwt_token_validator.py / agent_auth_configuration.py): the channel signs
// activity tokens as the Bot Framework issuer, validated against the ABS JWKS.
const (
	issuerBotFramework  = "https://api.botframework.com"
	jwksBotFrameworkURL = "https://login.botframework.com/v1/.well-known/keys"
	defaultLeeway       = 5 * time.Minute
)

// inboundValidator verifies Bot Framework activity JWTs. It accepts only the Bot
// Framework channel issuer and enforces RS256, audience == app ID, and expiry
// with leeway.
//
// This connector is a Teams *messaging* bot: real user activities always arrive
// signed by the Bot Framework channel (iss = api.botframework.com). We do NOT
// accept tokens issued directly by the tenant's AAD (sts.windows.net /
// login.microsoftonline.com) — that path exists in the SDK for agent-to-agent /
// skill invocation, which this connector does not offer. Restricting to the
// channel issuer keeps the activity `From` trustworthy (the channel sets it),
// so allow_from and the serviceURL binding rest on a signed identity rather than
// caller-supplied body fields. Restore an AAD path here if this bot is ever used
// as a skill target.
type inboundValidator struct {
	appID  string
	leeway time.Duration

	bfKeys jwt.Keyfunc // Bot Framework JWKS
}

// newInboundValidator wires the Bot Framework JWKS keyfunc with rotation caching.
func newInboundValidator(cfg config) (*inboundValidator, error) {
	bf, err := keyfunc.NewDefaultCtx(context.Background(), []string{jwksBotFrameworkURL})
	if err != nil {
		return nil, fmt.Errorf("teams: bot framework JWKS: %w", err)
	}
	return &inboundValidator{
		appID:  cfg.appID,
		leeway: defaultLeeway,
		bfKeys: bf.Keyfunc,
	}, nil
}

// keyFor returns the verification key only for the Bot Framework channel issuer,
// rejecting any other issuer before a signature check is attempted.
func (v *inboundValidator) keyFor(token *jwt.Token) (any, error) {
	iss, err := token.Claims.GetIssuer()
	if err != nil {
		return nil, fmt.Errorf("teams: token missing issuer: %w", err)
	}
	if iss == issuerBotFramework {
		return v.bfKeys(token)
	}
	return nil, fmt.Errorf("teams: untrusted issuer %q", iss)
}

// validate verifies a raw bearer token and returns its claims, or an error if
// the token fails any check.
func (v *inboundValidator) validate(tokenString string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, v.keyFor,
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithLeeway(v.leeway),
		jwt.WithAudience(v.appID),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("teams: token validation failed: %w", err)
	}
	return claims, nil
}
