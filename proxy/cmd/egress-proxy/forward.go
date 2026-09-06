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
// the agent saw an opaque "no sandbox backend is usable on this host" error
// rather than a policy verdict it could act on.
//
// Design rule (D5'): egress is fail-closed TO AN ATTESTED POLICY. This proxy
// is the only route out for non-attested traffic, and the resolved policy —
// the service ceiling, narrowed by the acting tenant's own — decides what it
// admits. Interposition beats prohibition: the fast path stays fast (a direct
// TLS tunnel, no MCP hop, no enclave round trip) while every connection is
// still named, policed and logged.
//
// What this is NOT, yet: enforcement. HTTP_PROXY is a convention, and a
// process that ignores it currently reaches the network directly, because the
// container's netns still masquerades everything (enclave-os network.go). The
// kernel default-drop + REDIRECT work is what turns this from
// honest-by-default into non-bypassable. Until then the verdicts here are true
// for cooperating clients and the log is complete for them; treat the posture
// as audited, not enforced.
//
// TLS is never terminated. CONNECT is policed by hostname and then spliced, so
// the enclave does not become a man in the middle of the user's traffic.

import (
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

// serveForward runs the forward-proxy listener. It speaks both proxy shapes a
// standard client uses: CONNECT for HTTPS, and absolute-form requests for
// plain HTTP.
func serveForward(listen string, store *policy.Store) {
	srv := &http.Server{
		Addr: listen,
		// No global timeouts: a CONNECT tunnel is long-lived by nature (git
		// clone, a slow download) and a write deadline would sever it.
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				forwardConnect(w, r, store)
				return
			}
			forwardPlain(w, r, store)
		}),
	}
	c := store.Ceiling()
	log.Printf("[egress-forward] listening on %s (ceiling mode=%s digest=%.16s…)", listen, c.Egress.Mode, c.Digest())
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[egress-forward] listen: %v", err)
	}
}

// actingEffective resolves the policy for the subject on whose behalf the
// shell is running.
//
// The forward path carries no credential of its own: it is the agent's own
// container reaching out, not an authenticated caller. Today the harness is
// single-user, so the relay-asserted subject bound at ingress IS the acting
// tenant. This function is the seam where multi-tenancy changes that — a
// per-user worker process will carry its own subject and pass it here, rather
// than the process-wide value (see subject.go).
func actingEffective(store *policy.Store) policy.Effective {
	return store.Effective(currentSubject())
}

// forwardConnect policies and then splices an HTTPS tunnel. The TLS session is
// end-to-end between the client and the origin: we see the hostname it asked
// for and nothing else.
func forwardConnect(w http.ResponseWriter, r *http.Request, store *policy.Store) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, port = r.Host, "443"
	}
	eff := actingEffective(store)
	if ok, why := eff.PermitsEgress(host, port); !ok {
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
	// The assurance label rides the audit line: a direct fetch is never green,
	// and the record of WHICH label applied is what makes a session's
	// per-step assurance reconstructable after the fact (D10).
	log.Printf("[egress-forward] ALLOW CONNECT %s:%s (mode=%s assurance=%s)",
		host, port, eff.Mode(), eff.AssuranceFor(host))

	// Splice both directions; the first side to end closes the tunnel.
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

// forwardPlain handles absolute-form HTTP (`GET http://host/path HTTP/1.1`),
// which is what a client sends to a proxy for cleartext requests.
func forwardPlain(w http.ResponseWriter, r *http.Request, store *policy.Store) {
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
	eff := actingEffective(store)
	if ok, why := eff.PermitsEgress(host, port); !ok {
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
	log.Printf("[egress-forward] ALLOW %s %s -> %d (mode=%s assurance=%s)",
		r.Method, r.URL.Host, resp.StatusCode, eff.Mode(), eff.AssuranceFor(host))
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
