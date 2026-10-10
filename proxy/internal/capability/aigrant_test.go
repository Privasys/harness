package capability

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

type edSigner struct{ priv ed25519.PrivateKey }

func (s edSigner) Sign(b []byte) ([]byte, error) { return ed25519.Sign(s.priv, b), nil }
func (s edSigner) PublicKeyB64() string {
	return base64.StdEncoding.EncodeToString(s.priv.Public().(ed25519.PublicKey))
}

func TestAIGrantToken(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	s := edSigner{priv}
	g := &Granted{CapabilityID: "cap-1", Status: "approved",
		ServiceResult: map[string]string{"tenant_id": "t-1", "grant_id": "g-1", "node_id": ""}}
	if g.Usable() {
		t.Fatal("a files.ai grant (no folder) passed as a folder grant")
	}
	if !g.UsableAI() {
		t.Fatal("an approved files.ai grant is not usable")
	}
	tok, err := AIGrantToken(s, g)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	body, _ := base64.RawURLEncoding.DecodeString(parts[0])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), body, sig) {
		t.Fatal("token not signed by the binding key")
	}
	var env envelope
	_ = json.Unmarshal(body, &env)
	if env.Aud != "privasys-drive" || env.Sub != "t-1" || env.Node != "" || env.JTI != "g-1" ||
		len(env.Scope) != 1 || env.Scope[0] != "read" {
		t.Fatalf("envelope %+v", env)
	}
	if _, err := AIGrantToken(s, &Granted{Status: "denied"}); err == nil {
		t.Fatal("a denied grant produced a token")
	}
}
