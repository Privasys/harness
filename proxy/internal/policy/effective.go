// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package policy

import "sort"

// Effective is what the proxy enforces for one acting subject: the service
// ceiling, and that subject's own policy where they have one.
//
// The narrowing invariant is implemented by CONJUNCTION rather than by
// computing a merged document. A request is admitted only if BOTH tiers admit
// it, which is exactly "a tenant may narrow, never widen" — and it avoids the
// one genuinely error-prone step, intersecting glob allowlists, where a subtle
// bug would silently widen what a user can reach. There is no merged egress
// document to get wrong.
type Effective struct {
	Ceiling *Document
	Tenant  *Document // nil when the subject has set no policy of their own
}

// PermitsEgress admits host:port only if both tiers do. The ceiling is tested
// first so its refusal is the one reported: it is the answer the user cannot
// change, and telling them the tier they could edit would be misleading.
func (e Effective) PermitsEgress(host, port string) (bool, string) {
	if ok, why := e.Ceiling.PermitsEgress(host, port); !ok {
		return false, why
	}
	if e.Tenant == nil {
		return true, ""
	}
	if ok, why := e.Tenant.PermitsEgress(host, port); !ok {
		return false, why + " [your own policy; the harness would otherwise permit this]"
	}
	return true, ""
}

// Mode reports the effective posture for display: the stricter of the two
// tiers. Used by the attestation panel and the verification API, never as the
// enforcement path — enforcement is PermitsEgress, which tests both documents
// rather than trusting this single label.
func (e Effective) Mode() Mode {
	if e.Ceiling == nil {
		return ModeNone
	}
	m := e.Ceiling.Egress.Mode
	if e.Tenant != nil && e.Tenant.Egress.Mode.rank() < m.rank() {
		m = e.Tenant.Egress.Mode
	}
	return m
}

// AttestedTools is the effective attested tool surface: those the ceiling
// admits, minus any the tenant has switched off. A tenant naming a tool the
// ceiling does not list gains nothing — the ceiling is the bound.
func (e Effective) AttestedTools() []ToolRef {
	if e.Ceiling == nil {
		return nil
	}
	if e.Tenant == nil || len(e.Tenant.Tools.Attested) == 0 {
		return e.Ceiling.Tools.Attested
	}
	keep := map[string]bool{}
	for _, t := range e.Tenant.Tools.Attested {
		keep[t.Name] = true
	}
	var out []ToolRef
	for _, t := range e.Ceiling.Tools.Attested {
		if keep[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// MCPServers is the effective bring-your-own surface. These are the tenant's
// to add, but only while the ceiling's posture admits reaching a non-attested
// host at all — an enterprise pinned to tee_only cannot have its users mount
// external servers, and this is where that falls out.
func (e Effective) MCPServers() []MCPServer {
	if e.Ceiling == nil || e.Ceiling.Egress.Mode.rank() <= ModeTeeOnly.rank() {
		return nil
	}
	var out []MCPServer
	out = append(out, e.Ceiling.Tools.MCP...)
	if e.Tenant != nil {
		out = append(out, e.Tenant.Tools.MCP...)
	}
	return out
}

// SpendCaps is what applies to one tool for one subject: the holder's own
// consent figures, narrowed by the ceiling's caps where the ceiling sets any.
type SpendCaps struct {
	// Consented is false when the holder has set no spend policy at all: no
	// priced call is admitted, whatever the ceiling would allow.
	Consented  bool   `json:"consented"`
	PerCall    uint64 `json:"per_call_max_credits"`
	PerSession uint64 `json:"per_session_max_credits"` // 0 = unbounded
}

// SpendCapsFor resolves the caps for one tool. Conjunction again: the
// tenant's figure and the ceiling's figure both bound the call, and the
// tenant must have set one — the ceiling never consents for a user.
func (e Effective) SpendCapsFor(tool string) SpendCaps {
	if e.Tenant == nil || e.Tenant.Spend == nil {
		return SpendCaps{}
	}
	caps := SpendCaps{
		Consented:  e.Tenant.Spend.PerCall(tool) > 0,
		PerCall:    e.Tenant.Spend.PerCall(tool),
		PerSession: e.Tenant.Spend.PerSessionMaxCredits,
	}
	if e.Ceiling != nil && e.Ceiling.Spend != nil {
		if c := e.Ceiling.Spend.PerCall(tool); c > 0 && (caps.PerCall == 0 || c < caps.PerCall) {
			caps.PerCall = c
		}
		if c := e.Ceiling.Spend.PerSessionMaxCredits; c > 0 && (caps.PerSession == 0 || c < caps.PerSession) {
			caps.PerSession = c
		}
	}
	return caps
}

// AdmitsSpend decides one priced call: credits is the price the attested
// runtime quoted, spent is the subject's running total this session. The
// reason is written for the agent to relay to the user, so it names the
// figure and where to change it.
func (e Effective) AdmitsSpend(tool string, credits, spent uint64) (bool, string) {
	caps := e.SpendCapsFor(tool)
	if !caps.Consented {
		return false, "this tool charges a fee and you have not approved any spending: set a per-call limit in the harness policy panel"
	}
	if credits > caps.PerCall {
		return false, "this call would charge more than the per-call limit you approved in the harness policy panel"
	}
	if caps.PerSession > 0 && spent+credits > caps.PerSession {
		return false, "this call would take this session past the spending limit you approved in the harness policy panel"
	}
	return true, ""
}

// ToolEnabled reports whether a built-in tool is active. Both tiers must admit
// it, and the image is the ceiling above both: a tool absent from the bundle
// cannot be switched on here at all.
func (e Effective) ToolEnabled(name string, dflt bool) bool {
	on := dflt
	if e.Ceiling != nil {
		if a, ok := e.Ceiling.Execution.Tools[name]; ok && a.Enabled != nil {
			on = *a.Enabled
		}
	}
	if !on {
		return false
	}
	if e.Tenant != nil {
		if a, ok := e.Tenant.Execution.Tools[name]; ok && a.Enabled != nil {
			on = *a.Enabled
		}
	}
	return on
}

// Assurance labels one peer for the trajectory. Generalises D10 from models to
// every peer: a non-attested peer is NEVER green, whatever anyone configures.
type Assurance string

const (
	// AssuranceGreen is mutual RA-TLS to a peer whose measurement matched the
	// declared dependency set.
	AssuranceGreen Assurance = "attested"
	// AssuranceAmber is TLS to a named host, optionally SPKI-pinned, that is
	// not attested.
	AssuranceAmber Assurance = "confidential-transport"
	// AssuranceGrey is any host, no pin.
	AssuranceGrey Assurance = "open"
)

// AssuranceFor labels one egress host under the effective policy. Hosts
// reached through the attested legs never come through here, so anything this
// classifies is amber at best.
func (e Effective) AssuranceFor(host string) Assurance {
	if e.Ceiling == nil {
		return AssuranceGrey
	}
	if HostMatches(host, e.Ceiling.Egress.Allowlist) {
		return AssuranceAmber
	}
	if e.Tenant != nil && HostMatches(host, e.Tenant.Egress.Allowlist) {
		return AssuranceAmber
	}
	for _, m := range e.MCPServers() {
		if m.Pin != nil && m.Pin.SPKISHA256 != "" {
			continue // pinned servers are amber, handled by host match above
		}
	}
	return AssuranceGrey
}

// Summary is the machine-readable view the verification API returns and the
// attestation panel renders. Both tiers are reported separately AND the
// effective posture is named, because "what this harness permits" and "what
// this session used" are different claims and conflating them is how an
// honest product acquires a false badge.
type Summary struct {
	Ceiling   *TierSummary `json:"ceiling"`
	Tenant    *TierSummary `json:"tenant"`
	Effective struct {
		Mode          Mode      `json:"mode"`
		AttestedTools []ToolRef `json:"attested_tools,omitempty"`
		MCPServers    []string  `json:"mcp_servers,omitempty"`
	} `json:"effective"`
}

// TierSummary describes one tier without disclosing anything a policy should
// not carry — credentials are vault references, never values.
type TierSummary struct {
	Digest   string   `json:"digest"`
	Scope    Scope    `json:"scope"`
	Subject  string   `json:"subject,omitempty"`
	IssuedAt string   `json:"issued_at,omitempty"`
	Mode     Mode     `json:"mode"`
	Allowlist []string `json:"allowlist,omitempty"`
}

func tierSummary(d *Document) *TierSummary {
	if d == nil {
		return nil
	}
	al := append([]string(nil), d.Egress.Allowlist...)
	sort.Strings(al)
	return &TierSummary{
		Digest:    d.Digest(),
		Scope:     d.Scope,
		Subject:   d.Subject,
		IssuedAt:  d.IssuedAt,
		Mode:      d.Egress.Mode,
		Allowlist: al,
	}
}

// Summarise builds the report for one acting subject.
func (e Effective) Summarise() Summary {
	var s Summary
	s.Ceiling = tierSummary(e.Ceiling)
	s.Tenant = tierSummary(e.Tenant)
	s.Effective.Mode = e.Mode()
	s.Effective.AttestedTools = e.AttestedTools()
	for _, m := range e.MCPServers() {
		s.Effective.MCPServers = append(s.Effective.MCPServers, m.Name)
	}
	return s
}
