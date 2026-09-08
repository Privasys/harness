// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

// Package capability is the harness's side of a user-owned resource: a
// folder of the holder's own Drive, approved on their device, in which their
// sessions and workspace live under their own keys instead of inside this
// enclave (plans/drive-as-remote-disk.md, D6′).
//
// The consent flow itself is no longer here. The enclave runtime brokers it
// (P2): it holds the per-app sealed Ed25519 binding key, keeps the pending
// ask, pushes the holder's wallet, serves the wallet's attested fetch on this
// app's hostname and receives the outcome. This package only asks the
// runtime over loopback and signs through it. The harness therefore holds no
// key and no per-user record of any kind: what remains on the enclave is the
// runtime's own state, which is exactly what P4 of the plan asks for.
package capability

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Signer mints the holder-of-key proof an AppGrant token carries. The
// runtime's broker is the only implementation in production; the key it
// signs with never leaves the manager.
type Signer interface {
	Sign(payload []byte) ([]byte, error)
	PublicKeyB64() string
}

// Granted is the outcome of an ask as the runtime reports it: approved with
// the resource service's coordinates, or denied (kept, so the app stops
// asking until the user reopens it).
type Granted struct {
	CapabilityID  string            `json:"capability_id,omitempty"`
	Status        string            `json:"status"`
	ServiceResult map[string]string `json:"service_result,omitempty"`
	At            time.Time         `json:"at,omitempty"`
}

// Usable reports whether this record can address a resource. Drive returns
// the coordinates in service_result; without them there is nothing to
// address.
func (g *Granted) Usable() bool {
	return g != nil && g.Status == "approved" && g.CapabilityID != "" &&
		g.ServiceResult["tenant_id"] != "" && g.ServiceResult["node_id"] != ""
}

// Denied reports a standing refusal.
func (g *Granted) Denied() bool { return g != nil && g.Status == "denied" }

// Path is where the resource service placed the folder, as it told the
// runtime (Drive: `AppData/<label>`), or "" before a grant exists.
func (g *Granted) Path() string {
	if g == nil {
		return ""
	}
	return g.ServiceResult["path"]
}

// Status is the runtime's view of one holder's resource.
type Status struct {
	Persistent  bool     `json:"persistent"`
	Declined    bool     `json:"declined"`
	Kind        string   `json:"kind"`
	Permissions []string `json:"permissions"`
	Label       string   `json:"label"`
	ResourceApp string   `json:"resource_app"`
	Granted     *Granted `json:"-"`
}

// Broker talks to the enclave runtime's resource endpoints:
//
//	POST /api/v1/resources/{resource}/request   {subject, retry}
//	GET  /api/v1/resources/{resource}/status?subject=
//	POST /api/v1/resources/sign                 {payload_b64}
//
// authenticated with the container token the runtime issued this process.
type Broker struct {
	url      string
	token    string
	resource string
	client   *http.Client

	mu    sync.Mutex
	pub   string
	cache map[string]cachedStatus
}

type cachedStatus struct {
	st *Status
	at time.Time
}

// statusTTL bounds how stale a cached status may be. The mirror asks every
// tick and the UI on every open; neither needs to reach the manager each
// time, but a fresh approval must show within a few seconds.
const statusTTL = 10 * time.Second

// NewBroker reads the runtime's coordinates from the container environment.
// resource is the name declared in the manifest's `resources` block.
func NewBroker(resource string) *Broker {
	return &Broker{
		url:      strings.TrimRight(os.Getenv("PRIVASYS_MANAGER_URL"), "/"),
		token:    os.Getenv("PRIVASYS_CONTAINER_TOKEN"),
		resource: resource,
		// Proxy:nil — the manager is on the GATEWAY IP, not loopback, and the
		// container environment sets HTTP_PROXY for the agent's shell. A bare
		// client would tunnel this control-plane call through our own egress
		// policy. Same trap as the dependency-set and stamp clients.
		client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}},
		cache:  map[string]cachedStatus{},
	}
}

// Enabled reports whether the runtime handed this process a manager and a
// token. Off-platform (a laptop) there is no broker and no persistence.
func (b *Broker) Enabled() bool {
	return b != nil && b.url != "" && b.token != "" && b.resource != ""
}

// Resource is the declared resource name this broker addresses.
func (b *Broker) Resource() string { return b.resource }

func (b *Broker) do(method, path string, body any) (int, []byte, error) {
	if !b.Enabled() {
		return 0, nil, errors.New("capability: no runtime broker (PRIVASYS_MANAGER_URL / PRIVASYS_CONTAINER_TOKEN unset)")
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.url+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// Request starts (or reports) an ask for one holder. The answer is the
// runtime's, passed through: `pending` with the nonce and this app's host,
// `already_granted`, or `declined`. retry is the user changing their mind,
// never the app trying again.
func (b *Broker) Request(subject string, retry bool) (map[string]any, error) {
	if subject == "" {
		return nil, errors.New("capability: a subject is required")
	}
	code, raw, err := b.do(http.MethodPost, "/api/v1/resources/"+url.PathEscape(b.resource)+"/request",
		map[string]any{"subject": subject, "retry": retry})
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("capability: runtime refused the request (HTTP %d: %s)", code, strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	b.Invalidate(subject)
	return out, nil
}

// Status returns the runtime's view of one holder's resource, cached briefly.
func (b *Broker) Status(subject string) (*Status, error) {
	if subject == "" {
		return nil, errors.New("capability: a subject is required")
	}
	b.mu.Lock()
	if c, ok := b.cache[subject]; ok && time.Since(c.at) < statusTTL {
		b.mu.Unlock()
		return c.st, nil
	}
	b.mu.Unlock()

	code, raw, err := b.do(http.MethodGet, "/api/v1/resources/"+url.PathEscape(b.resource)+"/status?subject="+url.QueryEscape(subject), nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("capability: runtime status failed (HTTP %d: %s)", code, strings.TrimSpace(string(raw)))
	}
	var wire struct {
		Status
		CapabilityID  string            `json:"capability_id"`
		ServiceResult map[string]string `json:"service_result"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	st := wire.Status
	switch {
	case wire.Persistent:
		st.Granted = &Granted{CapabilityID: wire.CapabilityID, Status: "approved", ServiceResult: wire.ServiceResult}
	case wire.Declined:
		st.Granted = &Granted{Status: "denied"}
	default:
		st.Granted = &Granted{}
	}
	b.mu.Lock()
	b.cache[subject] = cachedStatus{st: &st, at: time.Now()}
	b.mu.Unlock()
	return &st, nil
}

// Granted is Status reduced to the outcome. Never nil: a runtime that cannot
// be reached reads as "nothing granted", which is the truthful default for
// persistence.
func (b *Broker) Granted(subject string) *Granted {
	st, err := b.Status(subject)
	if err != nil || st == nil || st.Granted == nil {
		return &Granted{}
	}
	return st.Granted
}

// Invalidate drops the cached status for one holder, so the next read after
// an approval or a new ask reflects it.
func (b *Broker) Invalidate(subject string) {
	b.mu.Lock()
	delete(b.cache, subject)
	b.mu.Unlock()
}

// Sign asks the runtime for the holder-of-key proof over payload.
func (b *Broker) Sign(payload []byte) ([]byte, error) {
	code, raw, err := b.do(http.MethodPost, "/api/v1/resources/sign",
		map[string]string{"payload_b64": base64.StdEncoding.EncodeToString(payload)})
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("capability: runtime refused to sign (HTTP %d: %s)", code, strings.TrimSpace(string(raw)))
	}
	var out struct {
		SignatureB64 string `json:"signature_b64"`
		PubkeyB64    string `json:"pubkey_b64"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(out.SignatureB64)
	if err != nil {
		return nil, fmt.Errorf("capability: runtime signature is not base64: %w", err)
	}
	if out.PubkeyB64 != "" {
		b.mu.Lock()
		b.pub = out.PubkeyB64
		b.mu.Unlock()
	}
	return sig, nil
}

// PublicKeyB64 is the binding key's public half, learned from the runtime on
// first use. An AppGrant envelope carries it INSIDE the signed payload, so it
// has to be known before the payload is signed: one probe signature at start
// fetches it, and every real signature refreshes it.
func (b *Broker) PublicKeyB64() string {
	b.mu.Lock()
	pub := b.pub
	b.mu.Unlock()
	if pub != "" {
		return pub
	}
	if _, err := b.Sign([]byte("privasys-harness binding-key probe")); err != nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pub
}
