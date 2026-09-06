// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Publishing the policy digest into our own serving certificate.
//
// The plan's central move is "the policy engine is measured; the policy is
// attested by digest". This file is the second half: the proxy stamps the
// active SERVICE CEILING's digest into the leaf, so a relying party can check
// code X (the measurement) enforcing policy P (this extension), fetch P's full
// text from the verification API, and confirm it hashes to the stamped value.
//
// No new runtime capability was needed. The manager already exposes
// POST /api/v1/containers/{name}/attestation-extensions under
// requireContainerSelf, and `oids.ParseEnvVarOID` places any sub-arc under the
// app-defined 5.4 root — the same path Confidential AI uses for 5.4.7
// TOOLS_DIGEST. So this is a registry entry and a POST, not a platform change.
//
// Only the CEILING is stamped, never a tenant's policy. The leaf is one
// certificate serving every tenant, so a per-tenant value there would be
// meaningless at best and a cross-tenant leak at worst. A tenant's own policy
// is proved over the verification API inside their sealed session, where the
// channel already authenticates this enclave — see policy_api.go.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

// OIDPolicyDigest is WORKLOAD_POLICY_DIGEST: the SHA-256 of the active service
// ceiling's exact bytes. Registered in ra-tls-clients/oids.json alongside
// 5.4.5 MODEL_DIGEST and 5.4.7 TOOLS_DIGEST.
const OIDPolicyDigest = "1.3.6.1.4.1.65230.5.4.9"

// stamper publishes the ceiling digest to the enclave manager.
type stamper struct {
	managerURL string
	container  string
	token      string
	client     *http.Client
	last       string
}

func newStamper() *stamper {
	return &stamper{
		managerURL: strings.TrimRight(os.Getenv("PRIVASYS_MANAGER_URL"), "/"),
		container:  os.Getenv("PRIVASYS_CONTAINER_NAME"),
		token:      os.Getenv("PRIVASYS_CONTAINER_TOKEN"),
		// Proxy:nil — the manager is on the GATEWAY IP, not loopback, and the
		// container environment sets HTTP_PROXY for the agent's shell. A bare
		// client would tunnel this control-plane call through our own egress
		// policy. Same trap as the dependency-set client.
		client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}},
	}
}

func (s *stamper) enabled() bool {
	return s.managerURL != "" && s.container != "" && s.token != ""
}

// Stamp publishes one ceiling's digest. Idempotent: a repeat of the same value
// is skipped, because each successful call rewrites the extension file and
// force-reloads Caddy so the next leaf carries it.
func (s *stamper) Stamp(d *policy.Document) {
	if !s.enabled() || d == nil {
		return
	}
	digest := d.Digest()
	if digest == "" || digest == s.last {
		return
	}
	// The extension value is the raw 32 bytes, matching how 5.4.5 and 5.4.7
	// carry theirs — not the hex text. A verifier hex-encodes what it reads.
	raw, err := hex.DecodeString(digest)
	if err != nil {
		log.Printf("[policy stamp] digest %q is not hex: %v", digest, err)
		return
	}
	body, _ := json.Marshal(map[string]string{
		"oid":       OIDPolicyDigest,
		"value_b64": base64.StdEncoding.EncodeToString(raw),
	})
	url := fmt.Sprintf("%s/api/v1/containers/%s/attestation-extensions", s.managerURL, s.container)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("[policy stamp] build request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		// Non-fatal, and deliberately so: a harness that cannot publish its
		// digest is still enforcing the policy correctly. What it loses is the
		// ability to PROVE which policy to a third party, which is a
		// degradation of evidence, not of enforcement. Say so plainly rather
		// than refusing to serve.
		log.Printf("[policy stamp] could not publish the ceiling digest (enforcement unaffected, attestation of it is not): %v", err)
		return
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		log.Printf("[policy stamp] manager refused the ceiling digest: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		return
	}
	s.last = digest
	log.Printf("[policy stamp] ceiling digest published at %s (%.16s…); the next leaf carries it", OIDPolicyDigest, digest)
}
