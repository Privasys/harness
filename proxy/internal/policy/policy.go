// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

// Package policy is the harness's policy object and the engine that resolves
// it. It is the thing the measured Go proxy enforces, and the thing whose
// digest is attested — see .operations/plans/harness-policies.md.
//
// Two design rules carry the whole package:
//
//   - **The policy engine is measured; the policy is attested by digest.** We
//     do not need a measurement per posture. This code is in the image and so
//     in the code hash; what a verifier needs on top is WHICH policy it is
//     enforcing, which is the digest published on the leaf and served, with
//     its full text, from the verification API. A relying party checks: code
//     X, enforcing policy P, whose text hashes to P.
//
//   - **Each tier may only narrow what the tier above admits, never widen.**
//     The service ceiling (set by the app owner: Privasys, or an enterprise's
//     admins) bounds every tenant; a tenant's own policy narrows it further.
//     This one invariant is why enterprise administration needs no separate
//     mechanism, and why a verifier who checks only the ceiling already knows
//     the WORST case for a user's data.
//
// The digest is taken over the document's exact stored bytes, not over a
// re-serialisation. Canonicalisation is a source of verifier disagreement; a
// byte-for-byte hash of what we serve is unambiguous, and the verification API
// returns those same bytes.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// SchemaV1 is the only accepted `policy` value.
const SchemaV1 = "privasys.harness/v1"

// Scope distinguishes the two tiers this package resolves.
type Scope string

const (
	// ScopeCeiling is the app owner's policy: it bounds every tenant.
	ScopeCeiling Scope = "ceiling"
	// ScopeTenant is one user's own policy, which may only narrow the ceiling.
	ScopeTenant Scope = "tenant"
)

// Mode is the egress posture for traffic that is not an attested peer call.
// Names echo fleets.tool_policy so the platform keeps one vocabulary.
type Mode string

const (
	// ModeNone admits no egress at all: local tools only. Strongest posture.
	ModeNone Mode = "none"
	// ModeTeeOnly admits only the attested peers in the dependency set, which
	// this proxy reaches over mutual RA-TLS — never through the forward path.
	ModeTeeOnly Mode = "tee_only"
	// ModeAllowlist adds named hosts.
	ModeAllowlist Mode = "allowlist"
	// ModeProxy forwards everything to the customer's own firewall. Deferred
	// until a design partner needs it; parsed and ranked so a document written
	// against a later build is understood rather than silently mis-read.
	ModeProxy Mode = "proxy"
	// ModeOpen admits any host, still through this proxy and still logged.
	ModeOpen Mode = "open"
)

// rank orders the modes from strictest to most permissive. It is what makes
// "narrow, never widen" decidable: a tenant may select any mode whose rank is
// at most the ceiling's.
func (m Mode) rank() int {
	switch m {
	case ModeNone:
		return 0
	case ModeTeeOnly:
		return 1
	case ModeAllowlist:
		return 2
	case ModeProxy:
		return 3
	case ModeOpen:
		return 4
	}
	return -1 // unknown
}

// Valid reports whether the mode is one this build understands.
func (m Mode) Valid() bool { return m.rank() >= 0 }

// Egress is the non-attested egress posture.
type Egress struct {
	Mode          Mode     `json:"mode"`
	Allowlist     []string `json:"allowlist,omitempty"`
	UpstreamProxy string   `json:"upstream_proxy,omitempty"`
	DNS           string   `json:"dns,omitempty"`
}

// ToolRef names one agent-callable tool. Attested tools MUST also resolve in
// the dependency set (OID 7.1); this document cannot admit a peer the platform
// has not declared, it can only decline one.
type ToolRef struct {
	Name  string `json:"name"`
	AppID string `json:"app_id,omitempty"`
}

// MCPServer is a bring-your-own MCP server. HTTP only, and never green: a
// stdio server would be unmeasured code executing inside the trust boundary,
// which only a custom image build may introduce.
type MCPServer struct {
	Name       string   `json:"name"`
	Transport  string   `json:"transport"`
	URL        string   `json:"url"`
	Assurance  string   `json:"assurance,omitempty"`
	Pin        *Pin     `json:"pin,omitempty"`
	Credential string   `json:"credential,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
}

// Pin is an SPKI pin for a named, non-attested host.
type Pin struct {
	SPKISHA256 string `json:"spki_sha256,omitempty"`
}

// Tools is the agent-callable surface.
type Tools struct {
	Attested []ToolRef   `json:"attested,omitempty"`
	MCP      []MCPServer `json:"mcp,omitempty"`
}

// ToolActivation is one built-in tool's activation. The allow-list of which
// tools EXIST is in the image (tier 0); this only turns admitted ones on.
type ToolActivation struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Sandbox string `json:"sandbox,omitempty"`
}

// Execution is the in-TCB execution surface, bounded by the image.
type Execution struct {
	Tools     map[string]ToolActivation `json:"tools,omitempty"`
	Subagents *ToolActivation           `json:"subagents,omitempty"`
}

// Provider is one inference endpoint.
type Provider struct {
	ID         string `json:"id"`
	Assurance  string `json:"assurance"`
	AppID      string `json:"app_id,omitempty"`
	Host       string `json:"host,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Inference is the model surface.
type Inference struct {
	Default            string     `json:"default,omitempty"`
	Providers          []Provider `json:"providers,omitempty"`
	AllowOpenProviders bool       `json:"allow_open_providers,omitempty"`
	// NoSilentFallback keeps a refused provider refused. The adapter resolves
	// config -> env -> a PUBLIC default, so a missing endpoint once meant this
	// enclave's prompts could reach a third-party API with no attestation and
	// no gate. Never let that resolve quietly.
	NoSilentFallback bool `json:"no_silent_fallback,omitempty"`
}

// Data is derived, not configured: transcripts and workspace live on the
// acting user's Drive (D6'), and access, sharing and retention are Drive's to
// decide (D11). Kept in the document for legibility only.
type Data struct {
	Sessions  string `json:"sessions,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// Audit controls what the harness records about its own behaviour.
type Audit struct {
	EgressLog        bool `json:"egress_log,omitempty"`
	PerStepAssurance bool `json:"per_step_assurance,omitempty"`
}

// Spend is standing consent to per-call API fees (x-privasys.price). A priced
// tool call is admitted only when the holder's OWN document admits the
// exact price the attested runtime quoted, per call and within the running
// session total; the ceiling may cap these figures but can never grant on a
// user's behalf, because the fee is charged to the user ("the user pays",
// api-fees-plan §9). Consent given here is what the proxy turns into the
// byte-exact X-Billing-Approved header, so a successful priced call remains
// attestable proof that a person consented to that price — the person is
// the document's subject, and the document lives in their Drive.
//
// Credits are the platform's unit (1 credit = £0.00001 at the time of
// writing). Zero means "not set": a tenant document without a per-call
// figure admits no priced call at all.
type Spend struct {
	// PerCallMaxCredits admits any single call priced at or below it.
	PerCallMaxCredits uint64 `json:"per_call_max_credits,omitempty"`
	// PerSessionMaxCredits bounds the running total this process may charge
	// the subject; zero leaves the total unbounded (per-call still applies).
	PerSessionMaxCredits uint64 `json:"per_session_max_credits,omitempty"`
	// Tools overrides the per-call figure for one attested tool app, by the
	// name the harness mounts it under (web_search, web_reader, drive, …).
	Tools map[string]uint64 `json:"tools,omitempty"`
}

// PerCall returns the per-call figure that applies to one tool.
func (s *Spend) PerCall(tool string) uint64 {
	if s == nil {
		return 0
	}
	if v, ok := s.Tools[tool]; ok {
		return v
	}
	return s.PerCallMaxCredits
}

// Document is one policy at one tier.
type Document struct {
	Policy    string    `json:"policy"`
	Scope     Scope     `json:"scope"`
	Harness   string    `json:"harness,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	IssuedAt  string    `json:"issued_at,omitempty"`
	Execution Execution `json:"execution,omitempty"`
	Inference Inference `json:"inference,omitempty"`
	Tools     Tools     `json:"tools,omitempty"`
	Egress    Egress    `json:"egress"`
	Data      Data      `json:"data,omitempty"`
	Audit     Audit     `json:"audit,omitempty"`
	Spend     *Spend    `json:"spend,omitempty"`

	// raw is the exact byte sequence this document was parsed from, and the
	// preimage of Digest. Never re-serialised: see the package comment.
	raw []byte
}

// Raw returns the document's stored bytes — what the verification API serves
// and what Digest hashes.
func (d *Document) Raw() []byte {
	if d == nil {
		return nil
	}
	out := make([]byte, len(d.raw))
	copy(out, d.raw)
	return out
}

// Digest is the lowercase hex SHA-256 of Raw. This is the value stamped into
// the serving certificate for a ceiling, and reported by the verification API
// for both tiers.
func (d *Document) Digest() string {
	if d == nil || len(d.raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(d.raw)
	return hex.EncodeToString(sum[:])
}

// Parse reads and validates one policy document.
func Parse(raw []byte, want Scope) (*Document, error) {
	var d Document
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		// An unknown field is a REFUSAL, not a warning: a document written for
		// a later schema may narrow in ways this build cannot see, and
		// silently ignoring the field would enforce something more permissive
		// than its author wrote.
		return nil, fmt.Errorf("policy: parse: %w", err)
	}
	if d.Policy != SchemaV1 {
		return nil, fmt.Errorf("policy: unsupported schema %q (want %q)", d.Policy, SchemaV1)
	}
	if want != "" && d.Scope != want {
		return nil, fmt.Errorf("policy: scope %q, expected %q", d.Scope, want)
	}
	if !d.Egress.Mode.Valid() {
		return nil, fmt.Errorf("policy: unknown egress mode %q", d.Egress.Mode)
	}
	if d.Scope == ScopeTenant && d.Subject == "" {
		return nil, fmt.Errorf("policy: tenant document has no subject")
	}
	for _, m := range d.Tools.MCP {
		if m.Transport != "" && m.Transport != "http" {
			// A stdio server is an unmeasured child process inside the TCB.
			// Only a custom image build may introduce one, because only that
			// re-measures.
			return nil, fmt.Errorf("policy: mcp server %q: transport %q refused; HTTP only", m.Name, m.Transport)
		}
	}
	d.raw = append([]byte(nil), raw...)
	return &d, nil
}

// Bootstrap synthesises a ceiling from the measured image environment, for a
// deployment that has not been handed a policy document yet. It keeps a
// harness that predates the policy work behaving exactly as its image says,
// rather than failing closed on an absent file and taking the product down.
func Bootstrap(mode Mode, allowlist []string, harnessAppID string) *Document {
	if !mode.Valid() {
		// Same rule as the runtime's: an unrecognised posture reads as the
		// strictest, never as something more permissive.
		mode = ModeNone
	}
	// NO issued_at. The digest must be a pure function of the policy's
	// CONTENT: a bootstrap that stamped the boot time would change its
	// attested digest on every container restart while enforcing exactly the
	// same rules, so a verifier watching the leaf would see the policy "move"
	// for no reason and could not tell a real change from a redeploy.
	d := &Document{
		Policy:  SchemaV1,
		Scope:   ScopeCeiling,
		Harness: harnessAppID,
		Egress:  Egress{Mode: mode, Allowlist: allowlist},
		Data:    Data{Sessions: "drive_personal", Workspace: "drive"},
		Audit:   Audit{EgressLog: true, PerStepAssurance: true},
	}
	// Marshal ONCE and keep the bytes: the digest must be over exactly what we
	// serve, and re-marshalling later could reorder or reformat.
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		raw = []byte(`{"policy":"` + SchemaV1 + `","scope":"ceiling"}`)
	}
	d.raw = raw
	return d
}

// PermitsEgress answers whether this one document admits host:port, and why
// not when it does not. The reason is surfaced to the caller verbatim: an
// agent told "only attested enclave peers are reachable" can choose the
// attested tool instead, where a bare connection error teaches it nothing.
func (d *Document) PermitsEgress(host, port string) (bool, string) {
	if d == nil {
		return false, "no policy loaded"
	}
	switch d.Egress.Mode {
	case ModeNone:
		return false, "this harness has no egress (mode=none)"
	case ModeTeeOnly:
		return false, "only attested enclave peers are reachable from this harness (mode=tee_only); " +
			"use the attested tools rather than a direct fetch"
	case ModeOpen:
		return true, ""
	case ModeProxy:
		// The customer's own perimeter is the control. Host policy is theirs;
		// ours is to forward. Port discipline still applies.
		if port != "80" && port != "443" {
			return false, "only ports 80 and 443 are forwarded (mode=proxy)"
		}
		return true, ""
	case ModeAllowlist:
		// Outside open mode the port is constrained: an allowlist that admits
		// any port on a named host is a tunnel to anywhere that host forwards.
		if port != "80" && port != "443" {
			return false, "only ports 80 and 443 are permitted (mode=allowlist)"
		}
		if HostMatches(host, d.Egress.Allowlist) {
			return true, ""
		}
		return false, "host is not in this harness's egress allowlist (mode=allowlist)"
	}
	return false, "egress policy unavailable"
}

// HostMatches applies an allowlist. An entry is an exact hostname or a
// "*.example.com" wildcard, which matches the apex and any subdomain — the
// reading users expect, and the one NO_PROXY uses elsewhere.
func HostMatches(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == host {
			return true
		}
		if suffix, ok := strings.CutPrefix(e, "*."); ok {
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				return true
			}
		}
	}
	return false
}
