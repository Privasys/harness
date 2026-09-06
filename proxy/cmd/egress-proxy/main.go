// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

// egress-proxy is the attested egress leg of the harness app: a localhost
// listener through which every outbound attested call — Confidential AI
// inference, tool apps — must pass. It terminates plain HTTP from the dsh
// plugins and dials the peer over mutual RA-TLS (challenge extension via the
// Privasys Go fork), enforcing the app's declared dependency set (OID
// 65230.7.1) fail-closed.
//
// Design rule (D2): attestation authority never lives in Node. The dsh-side
// plugins route through this proxy and render its verdicts; they cannot
// weaken them.
//
// Routes:
//
//	GET  /healthz                 liveness + current dependency fold
//	ANY  /model/<path>            -> https://$HARNESS_MODEL_HOST/<path>
//	ANY  /tool/{name}/<path>      -> https://<host from $HARNESS_TOOL_HOSTS>/<path>
//
// Every upstream dial is attested per request (fresh challenge nonce, quote
// verification, declared-dependency gate, mutual client cert when the callee
// asks). A peer that is not in the 6.1 set is refused — the WS1 posture has
// no tool-grant passthrough yet (grantPinned is always false); per-session
// user tools arrive with WS2.
//
// Build: upstream Go (RA-TLS v2 needs no patched TLS stack).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	neturl "net/url"
	"os"
	"strings"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/attested"
	"github.com/Privasys/attested-harness/proxy/internal/capability"
	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

// splitList parses a comma-separated environment list, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type config struct {
	// listenAddr is the loopback address the dsh plugins call.
	listenAddr string
	// modelHost is the Confidential AI hostname behind /model/*.
	modelHost string
	// toolHosts maps a tool name (the /tool/{name}/ segment) to its
	// hostname. WS1: static from HARNESS_TOOL_HOSTS
	// ("drive=privasys-drive.apps.privasys.org,web_search=...");
	// WS2 replaces this with the fleet tool-spec.
	toolHosts map[string]string
	// onPlatform is true when the enclave manager is reachable
	// (PRIVASYS_MANAGER_URL set): the proxy then has an attested client
	// identity and dials peers with it, so CAI authenticates the harness as
	// an attested APP (X-Privasys-Peer-*) and the dsh-supplied bearer is
	// dropped. Off platform (dev) the bearer is the only credential and is
	// forwarded unchanged.
	onPlatform bool
	// ingressListen ($PORT via INGRESS_LISTEN) is the platform-facing port
	// the Go proxy fronts so health passes instantly while dsh boots;
	// dshUpstream is the loopback dsh web server it reverse-proxies to. Both
	// empty off platform (dev drives dsh directly).
	ingressListen string
	dshUpstream   string
	// forwardListen is the governed fast path for non-attested egress: the
	// address exported as HTTP_PROXY/HTTPS_PROXY into the agent's shell
	// tools. See forward.go for why interposition beats prohibition here.
	forwardListen string
}

func loadConfig() config {
	c := config{
		listenAddr:    envOr("EGRESS_PROXY_LISTEN", "127.0.0.1:9411"),
		modelHost:     os.Getenv("HARNESS_MODEL_HOST"),
		toolHosts:     map[string]string{},
		onPlatform:    os.Getenv("PRIVASYS_MANAGER_URL") != "",
		ingressListen: os.Getenv("INGRESS_LISTEN"),
		dshUpstream:   os.Getenv("DSH_UPSTREAM"),
		forwardListen: envOr("EGRESS_FORWARD_LISTEN", "127.0.0.1:9412"),
	}
	for _, kv := range strings.Split(os.Getenv("HARNESS_TOOL_HOSTS"), ",") {
		if name, host, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && name != "" && host != "" {
			c.toolHosts[name] = host
		}
	}
	return c
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// forward re-issues the inbound request against https://<host><path> over the
// attested client and streams the response back. The upstream transport does
// the whole trust dance; this function only rewrites the target.
func forward(w http.ResponseWriter, r *http.Request, client *http.Client, host, path string, repro bool) {
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	url := "https://" + host + path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"egress-proxy: build request: %v"}`, err), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	// The plugin-to-proxy hop is loopback: upstream compression only turns
	// the SSE stream into opaque bytes the repro scanner (and any future
	// in-proxy policy) cannot read. Plaintext end-to-end.
	req.Header.Del("Accept-Encoding")
	req.Host = host
	if repro {
		injectReproOptIn(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		// Attestation refusals land here: surface the exact verdict to the
		// plugin so the session log records WHY the leg was refused — AND log
		// it. Without this line a refused dial is completely silent on the
		// server: the caller sees only its own generic "HTTP 502" (dsh prints
		// the status, never our body), and the success path's "model leg"
		// line never runs, so the logs look like no request happened at all.
		// That combination cost a full day's debugging on 2026-09-05.
		log.Printf("[egress-proxy] %s leg REFUSED: %s %s -> %v",
			map[bool]string{true: "model", false: "tool"}[repro], r.Method, host, err)
		http.Error(w, fmt.Sprintf(`{"error":"egress-proxy: %v"}`, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body := resp.Body
	if repro {
		log.Printf("[egress-proxy] model leg: %s %s -> %d %s", r.Method, path, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if repro && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body = newReproScanBody(resp.Body)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// Flush per read so SSE deltas reach the plugin as they arrive instead
	// of buffering into one burst at stream end.
	if f, ok := w.(http.Flusher); ok {
		buf := make([]byte, 32<<10)
		for {
			n, rerr := body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				f.Flush()
			}
			if rerr != nil {
				return
			}
		}
	}
	io.Copy(w, body)
}

func main() {
	cfg := loadConfig()

	// The declared dependency set is the proxy's routing authority: refresh
	// it from the enclave manager for the life of the process. Off platform
	// (no PRIVASYS_MANAGER_URL) it stays disabled and only the legacy
	// per-host pins would apply — the WS1 proxy sets none, so every attested
	// dial then relies on quote verification alone (dev only).
	deps := attested.NewDepSet()
	deps.Start(time.Minute)

	transport := attested.NewRATLSTransport()
	transport.Deps = deps
	// A dependency-set change evicts pooled verified connections: the next
	// dial re-runs the gate against the new set.
	deps.OnChange = transport.CloseIdleConnections
	client := &http.Client{Transport: transport, Timeout: 5 * time.Minute}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":"ok","component":"egress-proxy","dependency_fold":%q}`+"\n", deps.Fold())
	})
	mux.HandleFunc("/model/", func(w http.ResponseWriter, r *http.Request) {
		if cfg.modelHost == "" {
			http.Error(w, `{"error":"egress-proxy: HARNESS_MODEL_HOST not configured"}`, http.StatusNotImplemented)
			return
		}
		// On-platform: drop dsh's Authorization bearer so CAI authenticates
		// the harness as an attested app (mutual RA-TLS peer identity), not a
		// token. Off platform the bearer is the only credential — keep it.
		if cfg.onPlatform {
			r.Header.Del("Authorization")
		}
		forward(w, r, client, cfg.modelHost, strings.TrimPrefix(r.URL.Path, "/model"), true)
	})
	mux.HandleFunc("/tool/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/tool/")
		name, path, _ := strings.Cut(rest, "/")
		host := cfg.toolHosts[name]
		if host == "" {
			http.Error(w, fmt.Sprintf(`{"error":"egress-proxy: unknown tool %q"}`, name), http.StatusNotFound)
			return
		}
		// /tool/{name}/mcp speaks MCP (Streamable HTTP) so dsh's stock
		// mcp-client mounts the tool app by configuration alone; any other
		// path forwards verbatim for direct callers.
		if path == "mcp" {
			mcpShim(w, r, client, name, host)
			return
		}
		forward(w, r, client, host, "/"+path, false)
	})
	// Anything else is a routing bug in the caller, never a passthrough.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"egress-proxy: unrouted path; only /model/* and /tool/{name}/* egress"}`, http.StatusNotFound)
	})

	log.Printf("[egress-proxy] listening on %s (model=%s tools=%d on_platform=%v deps_enabled=%v)",
		cfg.listenAddr, cfg.modelHost, len(cfg.toolHosts), cfg.onPlatform, deps.Enabled())

	// The policy object this proxy enforces: the service ceiling, narrowed at
	// each request by the acting tenant's own policy. A deployment that has
	// never been handed a document bootstraps its ceiling from the measured
	// image environment, so a harness predating this work behaves exactly as
	// its image says rather than failing closed on an absent file.
	store := policy.NewStore(envOr("HARNESS_POLICY_DIR", "/data/policy"))
	stamp := newStamper()
	store.OnCeilingChange = stamp.Stamp
	ceiling, err := store.LoadCeiling(
		envOr("HARNESS_CEILING_FILE", "/app/ceiling.json"),
		policy.Mode(envOr("HARNESS_EGRESS_MODE", string(policy.ModeTeeOnly))),
		splitList(os.Getenv("HARNESS_EGRESS_ALLOWLIST")),
		os.Getenv("HARNESS_APP_ID"),
	)
	if err != nil {
		// A ceiling that exists but cannot be parsed is fatal. Falling back to
		// the image default would turn a corrupt file into a WIDENING, which
		// is the one failure mode a policy engine must never have.
		log.Fatalf("[policy] %v", err)
	}
	stamp.Stamp(ceiling)

	// The capability identity and store: how this harness asks the holder for
	// authority over a folder of their own Drive, so their sessions persist
	// under their keys rather than inside this enclave (D6').
	capIdentity, err := capability.NewIdentity(envOr("HARNESS_POLICY_DIR", "/data/policy"))
	if err != nil {
		log.Fatalf("[capability] %v", err)
	}
	capStore := capability.NewStore(envOr("HARNESS_POLICY_DIR", "/data/policy"), capIdentity)

	// The governed fast path for everything that is not an attested peer
	// call. Started before ingress so the shell's HTTP_PROXY is answerable
	// from the moment dsh accepts a turn.
	if cfg.forwardListen != "" {
		go serveForward(cfg.forwardListen, store)
	}

	// Ingress front (INGRESS_LISTEN, the platform-allocated $PORT): the Go
	// proxy owns $PORT from second one so the platform health check passes
	// immediately while dsh (heavy, ~40s boot) comes up behind it — the same
	// pattern confidential-ai uses (Go front on $PORT, 503 until the backend
	// is ready). /healthz answers instantly; everything else reverse-proxies
	// to dsh on the loopback upstream, 503 until dsh is listening. Putting
	// ingress here too means the measured Go layer owns every network edge.
	if cfg.ingressListen != "" && cfg.dshUpstream != "" {
		go serveIngress(cfg, deps, store, stamp, capStore)
	}

	if err := http.ListenAndServe(cfg.listenAddr, mux); err != nil {
		log.Fatalf("[egress-proxy] listen: %v", err)
	}
}

// serveIngress fronts the platform port: instant health, the browser
// attestation summary, and a reverse-proxy to dsh once it is up.
func serveIngress(cfg config, deps *attested.DepSet, store *policy.Store, stamp *stamper, capStore *capability.Store) {
	listen, upstream := cfg.ingressListen, cfg.dshUpstream
	target, err := neturl.Parse(upstream)
	if err != nil {
		log.Fatalf("[ingress] bad DSH_UPSTREAM %q: %v", upstream, err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	// dsh guards /api with a DNS-rebinding + cross-site fence
	// (api-request-trust.ts): the Host header must be loopback or a
	// --trusted-host; a `sec-fetch-site: cross-site` marker is refused
	// outright; and any browser Origin must equal the Host authority. That
	// fence assumes a browser talking DIRECTLY to a local dsh over plain HTTP.
	// Our topology is different: the browser's sealed frames originate in the
	// Privasys IdP iframe (privasys.id) — a DIFFERENT origin from the app host
	// — so they carry `origin: https://privasys.id` and
	// `sec-fetch-site: cross-site`, both of which trip the fence (verified:
	// dsh returns 403 for either, and 200 once they are absent). The fence is
	// redundant here: the manager already terminated the sealed CBOR-AES-GCM
	// channel and authenticated an attested session before this loopback hop,
	// so there is no untrusted browser and no rebinding surface left. We
	// therefore (a) pin Host to the measured public host (a declared
	// --trusted-host), and (b) strip the browser-trust markers so dsh sees a
	// clean trusted-host, non-browser request — exactly the shape the fence
	// documents as allowed for remote/non-browser clients. The egress-proxy
	// runs inside the enclave TCB, so this rewrite is inside the trust
	// boundary, not a bypass of it.
	// Flush every write straight through: the /api/events.* SSE downlinks must
	// reach the sealing manager frame-by-frame, not buffered into a burst.
	rp.FlushInterval = -1
	publicHost := os.Getenv("HARNESS_PUBLIC_HOST")
	baseDirector := rp.Director
	rp.Director = func(req *http.Request) {
		baseDirector(req)
		// Bind the acting user: the sealed relay asserts the signed-in
		// subject on every unsealed request (see subject.go).
		recordSubject(req.Header.Get("X-Privasys-Sub"))
		if publicHost != "" {
			req.Host = publicHost
		}
		// Drop the cross-origin browser markers the sealed relay carries from
		// the IdP-iframe origin; without them the trusted Host alone satisfies
		// dsh's fence.
		req.Header.Del("Origin")
		req.Header.Del("Referer")
		req.Header.Del("Sec-Fetch-Site")
		req.Header.Del("Sec-Fetch-Mode")
		req.Header.Del("Sec-Fetch-Dest")
		req.Header.Del("Sec-Fetch-User")
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		// dsh not yet listening (still booting) — 503, so the platform's
		// health check on / can distinguish "starting" from "dead" while the
		// dedicated /healthz stays 200 to keep the container alive.
		http.Error(w, `{"status":"starting","component":"harness"}`, http.StatusServiceUnavailable)
	}
	mux := http.NewServeMux()
	// The platform container health check probes /health (mgmt
	// versions.go: http://localhost:$PORT/health) every 5s. Answer it here
	// instantly so the container stays alive while dsh boots behind the
	// proxy; /healthz is the same for anything using the conventional name.
	health := func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"ok","component":"harness-ingress"}`)
	}
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /healthz", health)
	// Browser attestation summary (reached over the sealed session by the
	// Privasys shell): the harness's own identity plus the live attested
	// dependency set the agent loop is fenced to. Read-only, no secrets.
	mux.HandleFunc("GET /privasys/attestation", func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"app": map[string]any{
				"public_host":  os.Getenv("HARNESS_PUBLIC_HOST"),
				"tee":          "Intel TDX",
				"image_digest": os.Getenv("PRIVASYS_IMAGE_DIGEST"),
				"app_id":       os.Getenv("HARNESS_APP_ID"),
			},
			"deps_enabled":    deps.Enabled(),
			"dependency_fold": deps.Fold(),
			"dependencies":    deps.Pinned(),
			"model_host":      cfg.modelHost,
			"tool_hosts":      cfg.toolHosts,
			// The non-attested egress posture. The panel must show this
			// beside the attested set, never instead of it: what this
			// harness PERMITS and what a session actually USED are
			// different claims, and conflating them is how an honest
			// product acquires a false badge.
			//
			// This is the CEILING, which is the same for every viewer and
			// safe on an anonymous endpoint. A caller's own effective
			// policy comes from /privasys/policy, which reads the
			// relay-asserted subject.
			"egress": map[string]any{
				"mode":      string(store.Ceiling().Egress.Mode),
				"allowlist": store.Ceiling().Egress.Allowlist,
			},
			"policy": map[string]any{
				"ceiling_digest": store.Ceiling().Digest(),
				"digest_oid":     OIDPolicyDigest,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	// The verification API: how a user checks which policy this enclave is
	// applying to them, and how a third party checks the product's posture.
	registerPolicyAPI(mux, store, stamp)
	registerCapabilityAPI(mux, capStore)
	mux.Handle("/", rp)
	log.Printf("[ingress] listening on %s -> %s", listen, upstream)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatalf("[ingress] listen: %v", err)
	}
}
