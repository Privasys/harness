// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

// Exercising an approved capability: reading and writing the holder's own
// Drive folder with a token signed by the binding key they approved. The key
// is the runtime's (per app, sealed on the manager's data volume); this
// process asks the runtime for each signature and never sees the private half.
//
// Two properties make this safe, and both are enforced on Drive's side:
//
//   - The token is a holder-of-key capability. Drive checks the embedded
//     public key against the binding the holder fixed at approval, so only
//     this app, on this runtime, can use the grant.
//   - The calls go out over the proxy's ATTESTED client identity. Drive then
//     also matches the enclave-os-verified peer app id against the grant's
//     subject, so a leaked key presented by a different app is refused. That
//     is why storage must ride the attested leg rather than opening its own
//     client: on an unattested connection Drive falls back to the key-only
//     check, which is strictly weaker.
//
// Scope is never widened here. The token requests exactly the permissions the
// holder saw on their approval screen; Drive independently confirms the grant
// carries them and that the node is inside the granted subtree.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	grantIssuer   = "https://privasys.id"
	grantAudience = "privasys-drive"
	// tokenTTL is deliberately short. The capability lives for as long as the
	// holder allowed; a TOKEN is a single working credential and there is no
	// reason for one to outlive the call it was minted for.
	tokenTTL = 5 * time.Minute
)

// envelope mirrors grants.Envelope on Drive's side. Field names and the
// signing input (the marshalled JSON) must match exactly, or the signature
// fails to verify.
type envelope struct {
	Iss   string   `json:"iss"`
	Aud   string   `json:"aud"`
	Sub   string   `json:"sub"`  // tenant id
	Node  string   `json:"node"` // node id
	Scope []string `json:"scope"`
	MRTD  string   `json:"mrtd"`
	JTI   string   `json:"jti"` // the grant id, for revocation lookup
	Iat   int64    `json:"iat"`
	Exp   int64    `json:"exp"`
	PK    string   `json:"pk"` // base64 ed25519 public key
}

// DriveStore performs file operations inside one approved folder.
//
// The caller supplies an http.Client whose transport is the proxy's attested
// RA-TLS client — see the package comment for why that is not optional.
type DriveStore struct {
	client  *http.Client
	host    string
	signer  Signer
	granted *Granted
}

// NewDriveStore binds a store to one holder's approved capability.
func NewDriveStore(client *http.Client, host string, signer Signer, g *Granted) (*DriveStore, error) {
	if !g.Usable() {
		return nil, errors.New("capability: no usable storage capability for this user")
	}
	if signer == nil {
		return nil, errors.New("capability: no signer for the capability's binding key")
	}
	return &DriveStore{client: client, host: host, signer: signer, granted: g}, nil
}

// RootID is the granted folder's node id.
func (d *DriveStore) RootID() string { return d.nodeID() }

func (d *DriveStore) tenantID() string { return d.granted.ServiceResult["tenant_id"] }
func (d *DriveStore) nodeID() string   { return d.granted.ServiceResult["node_id"] }

// grantID is the token's jti and Drive's revocation lookup key. Drive returns
// it in service_result; older records may carry only capability_id, which is
// the same value.
func (d *DriveStore) grantID() string {
	if g := d.granted.ServiceResult["grant_id"]; g != "" {
		return g
	}
	return d.granted.CapabilityID
}

// token mints a short-lived AppGrant credential for the requested scopes.
func (d *DriveStore) token(scopes []string) (string, error) {
	now := time.Now().UTC()
	env := envelope{
		Iss:   grantIssuer,
		Aud:   grantAudience,
		Sub:   d.tenantID(),
		Node:  d.nodeID(),
		Scope: scopes,
		JTI:   d.grantID(),
		Iat:   now.Unix(),
		Exp:   now.Add(tokenTTL).Unix(),
		PK:    d.signer.PublicKeyB64(),
	}
	if env.PK == "" {
		return "", errors.New("capability: the binding key's public half is not available from the runtime")
	}
	body, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	sig, err := d.signer.Sign(body)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

func (d *DriveStore) do(req *http.Request, scopes []string) (*http.Response, error) {
	tok, err := d.token(scopes)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "AppGrant "+tok)
	req.Host = d.host
	return d.client.Do(req)
}

// Node describes one entry in the approved folder.
type Node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
	Rev  int64  `json:"rev"`
}

// IsFolder reports whether a listed entry is a folder.
func (n Node) IsFolder() bool {
	return strings.EqualFold(n.Kind, "folder") || strings.EqualFold(n.Kind, "dir")
}

// ResolvePath looks a slash-separated path up under root (Drive D2). found is
// false when the path does not exist; any other failure is an error.
func (d *DriveStore) ResolvePath(root, path string) (Node, bool, error) {
	u := fmt.Sprintf("https://%s/v1/tenants/%s/path?root=%s&path=%s",
		d.host, url.PathEscape(d.tenantID()), url.QueryEscape(root), url.QueryEscape(path))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return Node{}, false, err
	}
	resp, err := d.do(req, []string{"read"})
	if err != nil {
		return Node{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Node{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Node{}, false, driveError("resolve "+path, resp)
	}
	var n Node
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return Node{}, false, err
	}
	return n, true, nil
}

// ReplaceContent overwrites one file's bytes in place (Drive D1), keeping the
// node — and therefore its id, its place in listings and its no-index mark.
func (d *DriveStore) ReplaceContent(fileID string, data []byte) error {
	u := fmt.Sprintf("https://%s/v1/tenants/%s/nodes/%s/content",
		d.host, url.PathEscape(d.tenantID()), url.PathEscape(fileID))
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := d.do(req, []string{"read", "write"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return driveError("replace content", resp)
	}
	return nil
}

// List returns the approved folder's direct children.
func (d *DriveStore) List() ([]Node, error) { return d.ListIn(d.nodeID()) }

// ListIn returns one folder's direct children. The node must be at or under
// the granted folder; Drive enforces that, not this code.
func (d *DriveStore) ListIn(nodeID string) ([]Node, error) {
	u := fmt.Sprintf("https://%s/v1/tenants/%s/folders/%s",
		d.host, url.PathEscape(d.tenantID()), url.PathEscape(nodeID))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.do(req, []string{"read"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, driveError("list", resp)
	}
	// Drive's folder listing has carried more than one envelope shape over
	// time; accept a bare array or a wrapper rather than coupling to one.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var direct []Node
	if json.Unmarshal(raw, &direct) == nil && direct != nil {
		return direct, nil
	}
	var wrapped struct {
		Nodes    []Node `json:"nodes"`
		Children []Node `json:"children"`
		Entries  []Node `json:"entries"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("capability: unrecognised folder listing: %w", err)
	}
	for _, set := range [][]Node{wrapped.Nodes, wrapped.Children, wrapped.Entries} {
		if len(set) > 0 {
			return set, nil
		}
	}
	return nil, nil
}

// EnsureFolder returns the id of a child folder, creating it when absent.
// Subfolders of the granted node are inside the granted subtree, so the same
// write scope admits them — which is what lets stored sessions keep dsh's own
// project/session shape instead of being flattened into one directory.
func (d *DriveStore) EnsureFolder(parentID, name string) (string, error) {
	children, err := d.ListIn(parentID)
	if err != nil {
		return "", err
	}
	for _, c := range children {
		if c.Name == name {
			return c.ID, nil
		}
	}
	body, _ := json.Marshal(map[string]string{"parent_id": parentID, "name": name})
	u := fmt.Sprintf("https://%s/v1/tenants/%s/folders", d.host, url.PathEscape(d.tenantID()))
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.do(req, []string{"read", "write"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", driveError("create folder "+name, resp)
	}
	var n Node
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return "", fmt.Errorf("capability: folder %q created but its id could not be read: %w", name, err)
	}
	return n.ID, nil
}

// PutIn writes one file into a named parent folder.
func (d *DriveStore) PutIn(parentID, name string, data []byte) (string, error) {
	return d.put(parentID, name, data)
}

// Put writes one file into the approved folder, replacing any file of the
// same name.
//
// `index=false`: these are session logs, not documents the holder asked to be
// searchable. Indexing them would push conversation text through the embedding
// pipeline and into the RAG surface without anyone choosing that.
func (d *DriveStore) Put(name string, data []byte) (string, error) {
	return d.put(d.nodeID(), name, data)
}

func (d *DriveStore) put(parentID, name string, data []byte) (string, error) {
	u := fmt.Sprintf("https://%s/v1/tenants/%s/files?name=%s&parent_id=%s&mime=%s&index=false",
		d.host, url.PathEscape(d.tenantID()), url.QueryEscape(name),
		url.QueryEscape(parentID), url.QueryEscape("application/octet-stream"))
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := d.do(req, []string{"read", "write"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		// A file of that name exists. Drive's create is create-only (the
		// (parent, name) pair is unique), so a changed file is REPLACED in
		// place rather than duplicated or, as before D1 existed, silently
		// left at its first version.
		existing, found, rerr := d.ResolvePath(parentID, name)
		if rerr != nil {
			return "", rerr
		}
		if !found {
			return "", driveError("upload "+name, resp)
		}
		if err := d.ReplaceContent(existing.ID, data); err != nil {
			return "", err
		}
		return existing.ID, nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", driveError("upload "+name, resp)
	}
	var n Node
	if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
		return "", nil // stored; the id is a convenience, not a requirement
	}
	return n.ID, nil
}

// Delete removes one node (a file, or a folder with its subtree) and
// reclaims its bytes. Needs the grant's `delete` permission; a grant without
// it answers 403, which surfaces as a RefusedError the caller must not read
// as a withdrawn capability.
func (d *DriveStore) Delete(nodeID string) error {
	u := fmt.Sprintf("https://%s/v1/tenants/%s/nodes/%s",
		d.host, url.PathEscape(d.tenantID()), url.PathEscape(nodeID))
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := d.do(req, []string{"delete"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return driveError("delete", resp)
	}
	return nil
}

// Get reads one file by its node id.
func (d *DriveStore) Get(fileID string) ([]byte, error) {
	u := fmt.Sprintf("https://%s/v1/tenants/%s/files/%s",
		d.host, url.PathEscape(d.tenantID()), url.PathEscape(fileID))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.do(req, []string{"read"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, driveError("download", resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// RefusedError is Drive refusing the capability itself (401/403): withdrawn
// by the holder, expired, or presented off the attested identity. Callers
// distinguish it from an outage because the two call for different words on
// screen — "connect again" versus "trying again".
type RefusedError struct {
	Op     string
	Status int
	Msg    string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("capability: Drive refused %s (HTTP %d: %s) — the capability may have been "+
		"revoked or expired, the call may not be riding the attested client identity (Drive then "+
		"matches the peer app id against the grant subject), or the target may be outside the "+
		"granted folder", e.Op, e.Status, e.Msg)
}

// IsRefused reports whether err is Drive refusing the capability.
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}

// driveError turns a Drive refusal into a sentence worth reading. A 403 here
// almost always means one of three things, and naming them saves the next
// person the investigation.
func driveError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	msg := strings.TrimSpace(string(body))
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return &RefusedError{Op: op, Status: resp.StatusCode, Msg: msg}
	}
	return fmt.Errorf("capability: Drive %s failed (HTTP %d: %s)", op, resp.StatusCode, msg)
}
