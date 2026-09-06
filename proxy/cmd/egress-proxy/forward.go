// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Forward proxy: the governed fast path for egress that is NOT an attested
// peer call — the agent's shell tools (curl, git, package managers) and any
// dsh plugin that honours HTTP_PROXY.
//
// Why this exists at all. The attested legs (/model, /tool/{name}) are the
// only egress a tee_only harness has, and they are mutual RA-TLS with the
// dependency-set gate in front. A shell cannot speak that. Before this file
// the shell had no route out, so dsh's sandbox refused to run unconfined and
// the agent saw an opaque "no sandbox backend is usable" error rather than a
// policy verdict it could act on.
//
// Design rule (D5'): egress is fail-closed TO AN ATTESTED POLICY. This proxy
// is the only route out for non-attested traffic, and the mode below decides
// what it admits. Interposition beats prohibition: the fast path stays fast
// (a direct TLS tunnel, no MCP hop, no enclave round trip) while every
// connection is still named, policed and logged.
//
// What this is NOT, yet: enforcement. HTTP_PROXY is a convention, and a
// process that ignores it currently reaches the network directly, because the
// container's netns still masquerades everything (enclave-os network.go). The
// kernel default-drop + REDIRECT work (know-how harness.md open item 4) is
// what turns this from honest-by-default into non-bypassable. Until then the
// verdicts here are true for cooperating clients and the log is complete for
// them; treat the posture as audited, not enforced.
//
// TLS is never terminated. CONNECT is policed by hostname and then spliced, so
// the enclave does not become a man in the middle of the user's traffic.

import (
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// egressMode is the harness's posture for non-attested egress. The names echo
// fleets.tool_policy so the platform keeps one vocabulary for permissiveness.
type egressMode string

const (
	// modeTeeOnly admits nothing here: the attested legs are the only egress.
	// This is the posture the deployed persona describes today.
	modeTeeOnly egressMode = "tee_only"
	// modeAllowlist admits the named hosts only.
	modeAllowlist egressMode = "allowlist"
	// modeOpen admits any host, and logs every one.
	modeOpen egressMode = "open"
	// modeNone admits nothing and is distinct from tee_only only in intent:
	// an air-gapped harness with no egress at all.
	modeNone egressMode = "none"
)

// egressPolicy is the live posture. WS1 reads it from the measured image
// environment; the tenant-policy work replaces this with a per-subject lookup
// resolved against the service ceiling (see .operations/plans/harness-policies.md).
type egressPolicy struct {
	mode      egressMode
	allowlist []string
}

func loadEgressPolicy() egressPolicy {
	p := egressPolicy{mode: egressMode(envOr("HARNESS_EGRESS_MODE", string(modeTeeOnly)))}
	switch p.mode {
	case modeTeeOnly, modeAllowlist, modeOpen, modeNone: // known
	default:
		// An unrecognised mode is a configuration error, and the only safe
		// reading of one is the strictest. (The platform has been bitten by
		// the opposite choice: fleets.tool_policy normalises unknown to
		// enclave_only in management and to locked in chat governance — same
		// string, two meanings. Fail strict, and say so.)
		log.Printf("[egress-forward] unknown HARNESS_EGRESS_MODE %q; falling back to %s", p.mode, modeNone)
		p.mode = modeNone
	}
	for _, h := range strings.Split(os.Getenv("HARNESS_EGRESS_ALLOWLIST"), ",") {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			p.allowlist = append(p.allowlist, h)
		}
	}
	return p
}

// permits answers whether one host:port may be reached, and why not when it
// may not. The reason is returned to the caller verbatim: an agent that is
// told "egress refused by policy (mode=tee_only)" can choose the attested
// web_reader tool instead, whereas a bare connection error teaches it nothing.
func (p egressPolicy) permits(host, port string) (bool, string) {
	switch p.mode {
	case modeNone:
		return false, "this harness has no egress (mode=none)"
	case modeTeeOnly:
		return false, "only attested enclave peers are reachable from this harness (mode=tee_only); " +
			"use the attested tools rather than a direct fetch"
	case modeOpen:
		return true, ""
	case modeAllowlist:
		// Ports are constrained outside open mode: an allowlist that admits
		// any port on a named host is a tunnel to anywhere that host will
		// forward to.
		if port != "80" && port != "443" {
			return false, "only ports 80 and 443 are permitted (mode=allowlist)"
		}
		if hostMatches(host, p.allowlist) {
			return true, ""
		}
		return false, "host is not in this harness's egress allowlist (mode=allowlist)"
	}
	return false, "egress policy unavailable"
}

// hostMatches applies the allowlist. An entry may be an exact hostname or a
// "*.example.com" wildcard, which matches the apex and any subdomain — the
// reading users expect, and the one NO_PROXY already uses elsewhere.
func hostMatches(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, e := range list {
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

// serveForward runs the forward-proxy listener. It speaks both proxy shapes a
// standard client uses: CONNECT for HTTPS, and absolute-form requests for
// plain HTTP.
func serveForward(listen string, p egressPolicy) {
	srv := &http.Server{
		Addr: listen,
		// No global timeouts: a CONNECT tunnel is long-lived by nature (git
		// clone, a slow download) and a write deadline would sever it.
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				forwardConnect(w, r, p)
				return
			}
			forwardPlain(w, r, p)
		}),
	}
	log.Printf("[egress-forward] listening on %s (mode=%s allowlist=%d)", listen, p.mode, len(p.allowlist))
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[egress-forward] listen: %v", err)
	}
}

// forwardConnect policies and then splices an HTTPS tunnel. The TLS session is
// end-to-end between the client and the origin: we see the hostname it asked
// for and nothing else.
func forwardConnect(w http.ResponseWriter, r *http.Request, p egressPolicy) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "443"
	}
	if ok, why := p.permits(host, port); !ok {
		log.Printf("[egress-forward] REFUSED CONNECT %s:%s -> %s", host, port, why)
		// 403 with the reason in the body. NOTE (verified 2026-09-06): curl
		// DISCARDS the body of a failed CONNECT and prints only "CONNECT
		// tunnel failed, response 403", so on HTTPS the agent learns that a
		// proxy refused it but not why. Plain HTTP (forwardPlain) does show
		// the sentence. Do not assume this text reaches the model on an
		// https:// URL; the container log is the complete record, and the
		// deployment persona carries the standing rule ("direct network
		// access is governed by policy — prefer the attested tools").
		http.Error(w, "egress refused by policy: "+why, http.StatusForbidden)
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 30*time.Second)
	if err != nil {
		log.Printf("[egress-forward] DIAL FAILED %s:%s -> %v", host, port, err)
		http.Error(w, "egress-forward: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "egress-forward: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		log.Printf("[egress-forward] hijack %s:%s: %v", host, port, err)
		return
	}
	defer client.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}
	log.Printf("[egress-forward] ALLOW CONNECT %s:%s (mode=%s)", host, port, p.mode)

	// Splice both directions; the first side to end closes the tunnel.
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

// forwardPlain handles absolute-form HTTP (`GET http://host/path HTTP/1.1`),
// which is what a client sends to a proxy for cleartext requests.
func forwardPlain(w http.ResponseWriter, r *http.Request, p egressPolicy) {
	if !r.URL.IsAbs() {
		// A relative request means something pointed a browser or a plugin at
		// this port directly. It is a proxy, not an origin.
		http.Error(w, `{"error":"egress-forward: this port is a forward proxy; set HTTP_PROXY rather than calling it directly"}`,
			http.StatusBadRequest)
		return
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	if port == "" {
		port = "80"
	}
	if ok, why := p.permits(host, port); !ok {
		log.Printf("[egress-forward] REFUSED %s %s -> %s", r.Method, r.URL.Host, why)
		http.Error(w, "egress refused by policy: "+why, http.StatusForbidden)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, "egress-forward: "+err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	// Hop-by-hop headers must not be forwarded.
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive",
		"Transfer-Encoding", "TE", "Trailer", "Upgrade"} {
		req.Header.Del(h)
	}
	// Proxy:nil — this client must never consult the environment and route
	// back into itself.
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   5 * time.Minute,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[egress-forward] UPSTREAM FAILED %s %s -> %v", r.Method, r.URL.Host, err)
		http.Error(w, "egress-forward: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	log.Printf("[egress-forward] ALLOW %s %s -> %d (mode=%s)", r.Method, r.URL.Host, resp.StatusCode, p.mode)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
