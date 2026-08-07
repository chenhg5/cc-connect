package teams

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testSigner builds tokens for a single RSA key and exposes a keyfunc that
// returns its public key, standing in for a JWKS endpoint.
type testSigner struct {
	key *rsa.PrivateKey
}

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &testSigner{key: key}
}

func (s *testSigner) keyfunc(*jwt.Token) (any, error) { return &s.key.PublicKey, nil }

func (s *testSigner) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(s.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func validatorWith(s *testSigner) *inboundValidator {
	return &inboundValidator{
		appID:  "app-123",
		leeway: defaultLeeway,
		bfKeys: s.keyfunc,
	}
}

func baseClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss": issuerBotFramework,
		"aud": "app-123",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
}

func TestValidate_AcceptsValidToken(t *testing.T) {
	s := newTestSigner(t)
	v := validatorWith(s)
	if _, err := v.validate(s.sign(t, baseClaims())); err != nil {
		t.Fatalf("expected valid token to pass, got %v", err)
	}
}

func TestValidate_RejectsWrongAudience(t *testing.T) {
	s := newTestSigner(t)
	v := validatorWith(s)
	c := baseClaims()
	c["aud"] = "someone-else"
	if _, err := v.validate(s.sign(t, c)); err == nil {
		t.Fatal("expected rejection for wrong audience")
	}
}

func TestValidate_RejectsExpired(t *testing.T) {
	s := newTestSigner(t)
	v := validatorWith(s)
	c := baseClaims()
	c["exp"] = time.Now().Add(-2 * defaultLeeway).Unix()
	if _, err := v.validate(s.sign(t, c)); err == nil {
		t.Fatal("expected rejection for expired token")
	}
}

func TestValidate_RejectsUntrustedIssuer(t *testing.T) {
	s := newTestSigner(t)
	v := validatorWith(s)
	c := baseClaims()
	c["iss"] = "https://evil.example.com"
	if _, err := v.validate(s.sign(t, c)); err == nil {
		t.Fatal("expected rejection for untrusted issuer")
	}
}

func TestValidate_RejectsBadSignature(t *testing.T) {
	signer := newTestSigner(t)
	other := newTestSigner(t)
	// Validator trusts `other`'s key, but the token is signed by `signer`.
	v := validatorWith(other)
	if _, err := v.validate(signer.sign(t, baseClaims())); err == nil {
		t.Fatal("expected rejection for token signed by an untrusted key")
	}
}

func TestValidate_RejectsNonRS256(t *testing.T) {
	s := newTestSigner(t)
	v := validatorWith(s)
	// HS256 token must be refused even if the alg-confusion key happened to match.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, baseClaims())
	hs, err := tok.SignedString([]byte("symmetric"))
	if err != nil {
		t.Fatalf("sign hs256: %v", err)
	}
	if _, err := v.validate(hs); err == nil {
		t.Fatal("expected rejection for non-RS256 token")
	}
}

func TestValidate_RejectsAADIssuer(t *testing.T) {
	s := newTestSigner(t)
	v := validatorWith(s)
	// AAD-issued tokens (skill / agent-to-agent path) are not accepted by this
	// messaging connector — only the Bot Framework channel issuer is trusted,
	// even when the signature would otherwise verify.
	for _, iss := range []string{
		"https://login.microsoftonline.com/tenant-xyz/v2.0",
		"https://sts.windows.net/tenant-xyz/",
	} {
		c := baseClaims()
		c["iss"] = iss
		if _, err := v.validate(s.sign(t, c)); err == nil {
			t.Fatalf("expected rejection for AAD issuer %q", iss)
		}
	}
}
