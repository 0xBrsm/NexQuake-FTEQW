package access

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// idpStub serves just enough OIDC (discovery + JWKS) for InitJWT to build a
// real verifier, plus mints RS256 id_tokens that verifier accepts. Signing is
// hand-rolled on the stdlib to avoid a go-jose test dependency. It's the shared
// harness for any access test that needs verifiable tokens (Parse, and later
// ExchangeCode).
type idpStub struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	kid      string
	issuer   string
	audience string
}

func newIDPStub(t *testing.T, audience string) *idpStub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s := &idpStub{key: key, kid: "k1", audience: audience}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStub(w, map[string]any{
			"issuer":                 s.issuer,
			"authorization_endpoint": s.issuer + "/authorize",
			"token_endpoint":         s.issuer + "/token",
			"jwks_uri":               s.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := s.key.PublicKey
		writeJSONStub(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": s.kid,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})

	s.server = httptest.NewServer(mux)
	s.issuer = s.server.URL
	t.Cleanup(s.server.Close)
	return s
}

// token mints a signed RS256 id_token with the standard claims.
func (s *idpStub) token(t *testing.T) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss":   s.issuer,
		"aud":   s.audience,
		"sub":   "user-1",
		"email": "admin@example.com",
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": s.kid}
	in := segStub(header) + "." + segStub(claims)
	digest := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func segStub(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSONStub(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// initVerifier wires a verifier against idp. headerName defaults to
// Authorization when empty.
func initVerifier(t *testing.T, idp *idpStub, headerName string) *JWTVerifier {
	t.Helper()
	t.Setenv("AUTH_ISSUER", idp.issuer)
	t.Setenv("AUTH_AUDIENCE", idp.audience)
	t.Setenv("AUTH_JWT_HEADER", headerName)
	t.Setenv("EXTERNAL_URL", "https://quake.example.com")
	j, err := InitJWT(context.Background())
	if err != nil {
		t.Fatalf("InitJWT: %v", err)
	}
	if j == nil {
		t.Fatal("InitJWT returned nil; expected a configured verifier")
	}
	return j
}
