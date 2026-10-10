package capability

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// A files.ai grant: read and search what the holder made available to AI in
// their Drive, acting for them. Drive takes the holder from the grant (the
// one who minted it), so the harness names nobody when it uses one: no
// X-Privasys-On-Behalf-Of, no shared identifier. Tenant-wide in shape (no
// node), confined by Drive to the AI-scope node set.

// UsableAI reports whether this record is an approved files.ai grant Drive
// can be addressed with: a tenant, a grant, and deliberately no folder.
func (g *Granted) UsableAI() bool {
	return g != nil && g.Status == "approved" && g.CapabilityID != "" &&
		g.ServiceResult["tenant_id"] != ""
}

// AIGrantToken mints a short-lived AppGrant credential for a files.ai grant,
// signed by the runtime-held binding key the grant was approved for.
func AIGrantToken(signer Signer, g *Granted) (string, error) {
	if !g.UsableAI() {
		return "", errors.New("capability: no usable files.ai grant for this user")
	}
	if signer == nil {
		return "", errors.New("capability: no signer for the grant's binding key")
	}
	pk := signer.PublicKeyB64()
	if pk == "" {
		return "", errors.New("capability: the binding key's public half is not available from the runtime")
	}
	now := time.Now().UTC()
	body, err := json.Marshal(envelope{
		Iss:   grantIssuer,
		Aud:   grantAudience,
		Sub:   g.ServiceResult["tenant_id"],
		Node:  "",
		Scope: []string{"read"},
		JTI:   g.GrantID(),
		Iat:   now.Unix(),
		Exp:   now.Add(tokenTTL).Unix(),
		PK:    pk,
	})
	if err != nil {
		return "", err
	}
	sig, err := signer.Sign(body)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
