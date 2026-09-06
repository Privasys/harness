// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package policy

import (
	"strings"
	"testing"
)

const ceilingOpen = `{
  "policy": "privasys.harness/v1",
  "scope": "ceiling",
  "egress": { "mode": "open" }
}`

const ceilingAllowlist = `{
  "policy": "privasys.harness/v1",
  "scope": "ceiling",
  "egress": { "mode": "allowlist", "allowlist": ["*.acme.com", "pypi.org"] }
}`

func mustParse(t *testing.T, raw string, scope Scope) *Document {
	t.Helper()
	d, err := Parse([]byte(raw), scope)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

// The digest must be over the document's exact bytes, so a verifier that
// fetches the text and hashes it reaches the same value we stamped.
func TestDigestIsOverRawBytes(t *testing.T) {
	raw := ceilingOpen
	d := mustParse(t, raw, ScopeCeiling)
	if got := string(d.Raw()); got != raw {
		t.Fatalf("Raw() must return the stored bytes verbatim:\n got %q\nwant %q", got, raw)
	}
	// Same semantic document, different whitespace -> different digest, by
	// design: the digest commits to the text a verifier can fetch, not to an
	// interpretation of it.
	spaced := strings.Replace(raw, `"mode": "open"`, `"mode":  "open"`, 1)
	if mustParse(t, spaced, ScopeCeiling).Digest() == d.Digest() {
		t.Fatal("documents with different bytes must have different digests")
	}
	if d.Digest() != mustParse(t, raw, ScopeCeiling).Digest() {
		t.Fatal("the same bytes must always digest to the same value")
	}
}

// An unknown field means a document written for a schema this build does not
// understand. Ignoring it could enforce something more permissive than its
// author wrote, so it is a refusal.
func TestParseRefusesUnknownFields(t *testing.T) {
	raw := `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"open"},"future_narrowing":{"x":1}}`
	if _, err := Parse([]byte(raw), ScopeCeiling); err == nil {
		t.Fatal("expected an unknown field to be refused")
	}
}

// A stdio MCP server is unmeasured code inside the trust boundary; only a
// custom image build may introduce one, because only that re-measures.
func TestParseRefusesStdioMCP(t *testing.T) {
	raw := `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"open"},
	         "tools":{"mcp":[{"name":"local","transport":"stdio","url":""}]}}`
	_, err := Parse([]byte(raw), ScopeCeiling)
	if err == nil || !strings.Contains(err.Error(), "HTTP only") {
		t.Fatalf("expected stdio to be refused, got %v", err)
	}
}

func TestParseRefusesTenantWithoutSubject(t *testing.T) {
	raw := `{"policy":"privasys.harness/v1","scope":"tenant","egress":{"mode":"none"}}`
	if _, err := Parse([]byte(raw), ScopeTenant); err == nil {
		t.Fatal("a tenant document without a subject must be refused")
	}
}

// The core invariant: a tenant may narrow, never widen.
func TestTenantCannotWidenAtRequestTime(t *testing.T) {
	e := Effective{
		Ceiling: mustParse(t, ceilingAllowlist, ScopeCeiling),
		Tenant: mustParse(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"u1",
		                        "egress":{"mode":"open"}}`, ScopeTenant),
	}
	// Even holding an "open" tenant document, a host the ceiling refuses stays
	// refused — enforcement is conjunction, not the tenant's label.
	if ok, _ := e.PermitsEgress("evil.example", "443"); ok {
		t.Fatal("tenant must not widen past the ceiling")
	}
	if ok, why := e.PermitsEgress("files.acme.com", "443"); !ok {
		t.Fatalf("ceiling-permitted host should pass: %s", why)
	}
}

func TestTenantNarrows(t *testing.T) {
	e := Effective{
		Ceiling: mustParse(t, ceilingOpen, ScopeCeiling),
		Tenant: mustParse(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"u1",
		                        "egress":{"mode":"allowlist","allowlist":["pypi.org"]}}`, ScopeTenant),
	}
	if ok, _ := e.PermitsEgress("pypi.org", "443"); !ok {
		t.Fatal("tenant allowlist entry should pass")
	}
	ok, why := e.PermitsEgress("anything.example", "443")
	if ok {
		t.Fatal("tenant narrowing must bind even under an open ceiling")
	}
	if !strings.Contains(why, "your own policy") {
		t.Fatalf("a tenant-tier refusal should say whose policy refused it, got %q", why)
	}
}

// The ceiling's refusal is the one reported: it is the tier the user cannot
// change, so naming the tenant tier would send them to edit the wrong thing.
func TestCeilingRefusalIsReportedFirst(t *testing.T) {
	e := Effective{
		Ceiling: mustParse(t, `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"tee_only"}}`, ScopeCeiling),
		Tenant: mustParse(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"u1",
		                        "egress":{"mode":"none"}}`, ScopeTenant),
	}
	_, why := e.PermitsEgress("pypi.org", "443")
	if !strings.Contains(why, "tee_only") {
		t.Fatalf("expected the ceiling's reason, got %q", why)
	}
}

func TestEffectiveModeIsTheStricter(t *testing.T) {
	e := Effective{
		Ceiling: mustParse(t, ceilingOpen, ScopeCeiling),
		Tenant: mustParse(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"u1",
		                        "egress":{"mode":"tee_only"}}`, ScopeTenant),
	}
	if got := e.Mode(); got != ModeTeeOnly {
		t.Fatalf("effective mode = %q, want tee_only", got)
	}
}

// Ports are constrained outside open mode: an allowlist admitting any port on
// a named host is a tunnel to anywhere that host will forward to.
func TestAllowlistConstrainsPorts(t *testing.T) {
	d := mustParse(t, ceilingAllowlist, ScopeCeiling)
	if ok, _ := d.PermitsEgress("pypi.org", "2222"); ok {
		t.Fatal("allowlist mode must not admit arbitrary ports")
	}
}

func TestHostMatchesWildcardCoversApexAndSubdomains(t *testing.T) {
	list := []string{"*.acme.com"}
	for _, h := range []string{"acme.com", "files.acme.com", "a.b.acme.com"} {
		if !HostMatches(h, list) {
			t.Fatalf("%q should match %v", h, list)
		}
	}
	for _, h := range []string{"acme.com.evil.net", "notacme.com"} {
		if HostMatches(h, list) {
			t.Fatalf("%q must NOT match %v", h, list)
		}
	}
}

// An unrecognised posture reads as the strictest, never as something more
// permissive — the opposite of the fleets.tool_policy divergence, where
// management normalises unknown to enclave_only and chat governance to locked.
func TestBootstrapUnknownModeFailsStrict(t *testing.T) {
	d := Bootstrap(Mode("wide-open"), nil, "app")
	if d.Egress.Mode != ModeNone {
		t.Fatalf("unknown mode bootstrapped to %q, want none", d.Egress.Mode)
	}
}

func TestNilTenantMeansCeilingOnly(t *testing.T) {
	e := Effective{Ceiling: mustParse(t, ceilingOpen, ScopeCeiling)}
	if ok, why := e.PermitsEgress("example.com", "443"); !ok {
		t.Fatalf("ceiling-only should permit: %s", why)
	}
}

// A harness whose ceiling admits no non-attested host cannot have its users
// mount external MCP servers, whatever their own policy says.
func TestTeeOnlyCeilingBlocksBYOMCP(t *testing.T) {
	e := Effective{
		Ceiling: mustParse(t, `{"policy":"privasys.harness/v1","scope":"ceiling","egress":{"mode":"tee_only"}}`, ScopeCeiling),
		Tenant: mustParse(t, `{"policy":"privasys.harness/v1","scope":"tenant","subject":"u1",
		                        "egress":{"mode":"tee_only"},
		                        "tools":{"mcp":[{"name":"crm","transport":"http","url":"https://mcp.acme.internal/mcp"}]}}`, ScopeTenant),
	}
	if got := e.MCPServers(); len(got) != 0 {
		t.Fatalf("tee_only ceiling must admit no BYO MCP servers, got %d", len(got))
	}
}

// A bootstrap ceiling's digest must be a pure function of its content. If it
// embedded the boot time, the attested value would change on every container
// restart while enforcing identical rules, and a verifier watching the leaf
// could not distinguish a real policy change from a redeploy.
func TestBootstrapDigestIsStableAcrossRestarts(t *testing.T) {
	a := Bootstrap(ModeAllowlist, []string{"*.acme.com"}, "app-1")
	b := Bootstrap(ModeAllowlist, []string{"*.acme.com"}, "app-1")
	if a.Digest() != b.Digest() {
		t.Fatalf("bootstrap digest is not stable:\n %s\n %s", a.Digest(), b.Digest())
	}
	c := Bootstrap(ModeOpen, []string{"*.acme.com"}, "app-1")
	if a.Digest() == c.Digest() {
		t.Fatal("a different posture must produce a different digest")
	}
}
