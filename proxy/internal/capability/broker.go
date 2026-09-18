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
	"bufio"
	"bytes"
	"context"
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

// GrantID is the resource service's revocation key for this grant (Drive
// returns it in service_result; older records carry only capability_id,
// the same value). Two approvals are two grants.
func (g *Granted) GrantID() string {
	if g == nil {
		return ""
	}
	if id := g.ServiceResult["grant_id"]; id != "" {
		return id
	}
	return g.CapabilityID
}

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
	// Stale: the grant was approved under other permissions than the
	// manifest declares now (runtime ≥ the 2026-09-11 manager fix reports
	// it); GrantedPermissions is what the holder actually approved. The
	// grant keeps working; the holder is asked again.
	Stale              bool     `json:"stale"`
	GrantedPermissions []string `json:"granted_permissions"`
	Granted            *Granted `json:"-"`
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

// ---- holder folders --------------------------------------------------------

// HolderFolder is the runtime's answer to an open: the holder's folder, one
// directory of this app's volume encrypted with the holder's own key, which
// the runtime opens under the holder's consent and this process never keys.
type HolderFolder struct {
	// Status is "open", "needs_holder" (the holder's wallet has been asked,
	// or must deliver the key again), "declined", "revoked", or
	// "unavailable" (a runtime without holder folders, or an app volume
	// that cannot carry them).
	Status string
	// Path is the folder inside this container when Status is "open".
	Path string
	// Nonce and AppHost identify the outstanding ask when Status is
	// "needs_holder".
	Nonce, AppHost string
}

// OpenHolderFolder asks the runtime for the holder's folder, owned by uid
// (this process's worker for that holder).
func (b *Broker) OpenHolderFolder(subject string, uid int) (*HolderFolder, error) {
	if subject == "" {
		return nil, errors.New("capability: a subject is required")
	}
	code, raw, err := b.do(http.MethodPost, "/api/v1/resources/"+url.PathEscape(b.resource)+"/open",
		map[string]any{"subject": subject, "uid": uid})
	if err != nil {
		return nil, err
	}
	var body struct {
		Status  string `json:"status"`
		Path    string `json:"path"`
		Nonce   string `json:"nonce"`
		AppHost string `json:"app_host"`
	}
	_ = json.Unmarshal(raw, &body)
	switch code {
	case http.StatusOK:
		if body.Path == "" {
			return nil, errors.New("capability: the runtime opened the folder but named no path")
		}
		return &HolderFolder{Status: "open", Path: body.Path}, nil
	case http.StatusConflict:
		return &HolderFolder{Status: "needs_holder", Nonce: body.Nonce, AppHost: body.AppHost}, nil
	case http.StatusForbidden:
		if body.Status == "" {
			body.Status = "declined"
		}
		return &HolderFolder{Status: body.Status}, nil
	case http.StatusNotFound, http.StatusNotImplemented:
		return &HolderFolder{Status: "unavailable"}, nil
	}
	return nil, fmt.Errorf("capability: open holder folder: HTTP %d: %s", code, strings.TrimSpace(string(raw)))
}

// CloseHolderFolder tells the runtime this process is done with the holder's
// folder; the runtime removes the key and the folder is locked. busy reports
// that files were still in use and the key is not fully gone yet.
func (b *Broker) CloseHolderFolder(subject string) (busy bool, err error) {
	code, raw, err := b.do(http.MethodPost, "/api/v1/resources/"+url.PathEscape(b.resource)+"/close",
		map[string]any{"subject": subject})
	if err != nil {
		return false, err
	}
	switch code {
	case http.StatusOK:
		return false, nil
	case http.StatusAccepted:
		return true, nil
	}
	return false, fmt.Errorf("capability: close holder folder: HTTP %d: %s", code, strings.TrimSpace(string(raw)))
}

// ---- subjects and events ----------------------------------------------------

// Subject is one holder with a recorded outcome for the resource.
type Subject struct {
	Subject      string    `json:"subject"`
	Status       string    `json:"status"`
	CapabilityID string    `json:"capability_id"`
	At           time.Time `json:"at"`
}

// Subjects lists the holders the runtime has an outcome for, so this process
// keeps no record of its own of who uses it. A runtime without the route
// answers an empty list and no error.
func (b *Broker) Subjects() ([]Subject, error) {
	code, raw, err := b.do(http.MethodGet, "/api/v1/resources/"+url.PathEscape(b.resource)+"/subjects", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, nil
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("capability: subjects: HTTP %d: %s", code, strings.TrimSpace(string(raw)))
	}
	var body struct {
		Subjects []Subject `json:"subjects"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body.Subjects, nil
}

// Event is one line of the runtime's stream: an approval, a denial, a
// revoke, a holder folder opened or closed.
type Event struct {
	Type         string `json:"-"`
	Resource     string `json:"resource"`
	Subject      string `json:"subject"`
	CapabilityID string `json:"capability_id"`
	At           string `json:"at"`
}

// Forget drops the cached status of a subject, so the next Status asks the
// runtime: what an event about that subject means for this process.
func (b *Broker) Forget(subject string) {
	b.mu.Lock()
	delete(b.cache, subject)
	b.mu.Unlock()
}

// Events follows the runtime's event stream until ctx ends, calling fn for
// each event. The stream carries every resource of this app. A runtime
// without the route (404) ends the loop quietly: there is nothing to listen
// to, and the callers poll as before. Any other break reconnects with
// backoff; nothing here ever polls the status.
func (b *Broker) Events(ctx context.Context, fn func(Event)) {
	if !b.Enabled() {
		return
	}
	wait := time.Second
	for ctx.Err() == nil {
		err := b.follow(ctx, fn)
		if errors.Is(err, errNoEvents) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < 30*time.Second {
			wait *= 2
		}
	}
}

var errNoEvents = errors.New("capability: the runtime has no event stream")

func (b *Broker) follow(ctx context.Context, fn func(Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url+"/api/v1/resources/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "text/event-stream")
	// No timeout on a stream; the transport is the same proxy-less one.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNoEvents
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("capability: events: HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	var typ string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			typ = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var ev Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil && typ != "" {
				ev.Type = typ
				fn(ev)
			}
			typ = ""
		case line == "":
			typ = ""
		}
	}
	return sc.Err()
}
