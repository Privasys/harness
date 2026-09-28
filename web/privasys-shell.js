// Privasys Harness — browser auth + attestation shell.
//
// This is the Privasys fork layer over the stock dsh web UI. It runs as a
// SEPARATE bundle from dsh (its own DOM roots, no shared framework) and owns
// three things the stock UI cannot do on the confidential platform:
//
//   1. Sign-in gate. The enclave gateway enforces sealed transport
//      (X-Privasys-Edge: terminate -> manager requireSealed), so a plain
//      browser load is 403'd "sealed-transport-required". We gate the whole
//      app behind a Privasys wallet/passkey/social ceremony (@privasys/auth
//      AuthFrame) that also establishes a sealed CBOR-AES-GCM session against
//      the harness enclave — the same transport chat.privasys.org uses.
//
//   2. Sealed transport hand-off. On a live sealed session we publish it to
//      dsh via `window.__PRIVASYS_SEALED__` and boot dsh through
//      `window.__PRIVASYS_BOOT__` (installed by the patched apps/web/main.ts).
//      dsh's connection plugin then routes its whole API — unary RPC + the
//      SSE event downlinks — through the sealed session instead of plain
//      fetch + WebSockets (which cannot traverse the sealed relay).
//
//   3. Attestation panel. A shield in the top-right opens a drawer that shows
//      the live evidence: the enclave your wallet attested at sign-in, the
//      harness measurement, and the attested dependency set (Confidential AI +
//      each tool) the agent loop is pinned to. "Trust you can verify."
//
// The SDK's frame-client is loaded first as a classic <script>
// (vendor/privasys-auth-client.iife.js -> window.Privasys); this module uses
// window.Privasys.AuthFrame. No bundler, no npm install in the enclave image.

/** @typedef {import('@privasys/auth').SealedSession} SealedSession */

// --- configuration ---------------------------------------------------------
// Deploy-time overridable via a `window.__PRIVASYS_CFG__ = {...}` <script>
// injected into index.html (see the Dockerfile / entrypoint). Defaults target
// the dev control plane + the apps.test enclave gateway the harness runs on.
const CFG = Object.assign(
    {
        // Management-service API base (session-relay metadata, attribute billing).
        apiBase: 'https://api-test.privasys.org',
        // Hosted auth iframe origin (privasys.id OIDC PKCE + wallet broker).
        authOrigin: 'https://privasys.id',
        // OIDC relying-party id (the IdP), NOT the app.
        rpId: 'privasys.id',
        // Shared platform OIDC client.
        clientId: 'privasys-platform',
        // Human name the SDK puts in the QR descriptor / wallet push — the
        // wallet SNAPSHOTS this string into its session cards and consent
        // records (it never resolves the app id itself), so it must be the
        // product name, never an identifier.
        appName: 'Privasys Harness',
        // The app as registered on the platform (attest-report lookups).
        appId: '590ebdc3-1b63-401f-bbb8-22d5f3886c5e',
        // The enclave host the sealed session is attested against.
        appHost: 'attested-harness.apps.test.privasys.org',
        // Control plane that owns THIS app's row (the /attest report) — the
        // dev CP for the apps.test harness. Anonymous endpoint.
        attestBase: 'https://api.developer.test.privasys.org',
        // Attestation server quote verification (needs an audience token).
        verifyQuoteUrl: 'https://as.privasys.org/verify-quote',
        // Broker relay for the wallet QR / push channel.
        brokerUrl: 'wss://relay.privasys.org/relay'
    },
    /** @type {any} */ (window).__PRIVASYS_CFG__ || {}
);

const AuthFrame = /** @type {any} */ (window).Privasys?.AuthFrame;

// --- DOM scaffolding -------------------------------------------------------
const shellRoot = document.getElementById('privasys-shell');
if (!shellRoot) throw new Error('privasys-shell: missing #privasys-shell mount');

function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    if (attrs) {
        for (const [k, v] of Object.entries(attrs)) {
            if (k === 'class') node.className = v;
            else if (k === 'style') node.setAttribute('style', v);
            else if (k.startsWith('on') && typeof v === 'function') node.addEventListener(k.slice(2), v);
            else if (v != null) node.setAttribute(k, String(v));
        }
    }
    for (const c of children) {
        if (c == null) continue;
        node.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
    }
    return node;
}

// Full-viewport gate container the SDK renders its ceremony into.
const gate = el('div', { id: 'privasys-gate', class: 'pv-gate' });
// Persistent top-right chrome (attestation shield) shown once signed in.
const chrome = el('div', { id: 'privasys-chrome', class: 'pv-chrome pv-hidden' });
shellRoot.appendChild(gate);
shellRoot.appendChild(chrome);

function setGate(...children) {
    gate.replaceChildren(...children);
    gate.classList.remove('pv-hidden');
}
function hideGate() {
    gate.classList.add('pv-hidden');
    gate.replaceChildren();
}

// --- sign-in flow ----------------------------------------------------------
let sealed = /** @type {SealedSession | null} */ (null);
// The live AuthFrame instance, hoisted so sign-out can clear its session.
let frame = /** @type {any} */ (null);

// Pitch strings for the SDK gate's left panel (page presentation). The SDK
// styles them; we supply copy only — the header, the "Secured by Privasys ID"
// seal, the ceremony states, and the terms footer are all the SDK's global,
// branded UI. We render NO chrome of our own around it (mirrors
// chat.privasys.org's SignInGate).
const HARNESS_PITCH = {
    // Brand lockup above the pitch title. Passing a pitch replaces the SDK's
    // default brand panel (logo + wordmark + ceremony status), so the pitch
    // must carry its own logo or the gate loses all branding on the left.
    logoUrl: location.origin + '/privasys/privasys-harness-logo.svg',
    title: 'A coding agent you can verify.',
    description:
        'The Privasys Harness runs its agent and model inside hardware-protected ' +
        'enclaves. The operator can never read your prompts or the agent’s work, ' +
        'and you can verify it yourself by remote attestation.',
    bullets: [
        'Sealed browser-to-enclave transport. The gateway only sees ciphertext.',
        'Every model call is attested Confidential AI. No bearer keys.',
        'Each tool the agent uses is a separately verified enclave.',
        'No passwords. Sign in with the Privasys Wallet on your phone.'
    ]
};

async function run() {
    if (!AuthFrame) {
        showAuthFallback(
            'The Privasys authentication script did not load. Reload the page.',
            false
        );
        return;
    }

    // One container; the SDK's `page` presentation fills it with the ENTIRE
    // branded surface when an interactive ceremony is needed. But during
    // connect()'s silent restore AND the one-tap push re-approval (relay TTL
    // expired -> wallet push, no browser interaction) the SDK paints nothing —
    // a blank white page for as long as the approval takes. The SDK exposes no
    // status events, so: keep a NON-BLOCKING connecting toast (pointer-events:
    // none, top-centred — a real ceremony iframe underneath stays fully
    // usable) until connect() settles, whatever the flow.
    gate.replaceChildren();
    gate.classList.remove('pv-hidden');
    const connecting = el('div', { class: 'pv-connecting' },
        spinnerCard('Connecting…',
            'Restoring your secure session. If your phone shows a Privasys ' +
            'approval request, approve it there.'));
    gate.appendChild(connecting);
    const clearConnecting = () => { connecting.remove(); };

    frame = new AuthFrame({
        apiBase: CFG.apiBase,
        authOrigin: CFG.authOrigin,
        rpId: CFG.rpId,
        clientId: CFG.clientId,
        appName: CFG.appName,
        brokerUrl: CFG.brokerUrl,
        // Data minimisation: the harness needs the user's Display Name (the
        // `name` attribute, scope `profile`) and nothing else — no email.
        // The SDK's audience-token mint (getTokenForAudience, used to verify
        // the attestation quote signature) rides the session's own granted
        // scopes as of sdk-v0.11.1, so nothing forces a wider grant here.
        scope: ['openid', 'profile', 'offline_access'],
        // Spend consent (acting-subject plan v2): the harness pays for the
        // user's inference and priced tools with the USER's credits, under
        // a monthly cap the wallet asks them to approve at sign-in.
        // Suggested cap £5 (credits, 1 credit = £0.000001).
        spend: { cap: 5000000 },
        container: gate,
        presentation: 'page',
        // Sealed instance: the end-to-end encrypted session is established only
        // over the wallet-attested channel (same constraint as chat).
        methods: ['wallet'],
        pitch: HARNESS_PITCH,
        // App identity for the gate header + the "Secured by Privasys ID" seal.
        app: {
            displayName: 'Privasys Harness',
            logoUrl: location.origin + '/privasys/privasys-logo.mini.svg'
        },
        // Establish the sealed transport against the harness enclave.
        sessionRelay: { appHost: CFG.appHost }
    });

    try {
        // connect(): silent restore -> one-tap re-approval -> full ceremony,
        // all rendered by the SDK. Resolves with the sealed session once the
        // enclave is attested and the transport is live.
        const res = await frame.connect();
        clearConnecting();
        // The user's Display Name (`name` claim, granted under the `profile`
        // scope) rides the session access token — decode it for the sidebar
        // User row. Presentation only: authorisation stays claim-blind.
        userDisplayName = claimFromJwt(res.accessToken, 'name');
        if (!res.session) {
            showAuthFallback(
                'Signed in, but the enclave returned no sealed session. The harness ' +
                'requires end-to-end encryption. Please try again.',
                false
            );
            return;
        }
        sealed = res.session;
        onAuthenticated();
    } catch (err) {
        clearConnecting();
        const code = /** @type {any} */ (err)?.code;
        if (code === 'cancelled') {
            showAuthFallback('Sign-in was closed.', true);
            return;
        }
        showAuthFallback(
            String(/** @type {any} */ (err)?.message || err || 'Sign-in failed') + '.',
            false
        );
    }
}

// Minimal fallback shown ONLY on error/cancel (never during the normal SDK
// flow) — a single button that re-enters the SDK gate. Mirrors chat's
// closed/error panel.
function showAuthFallback(message, closed) {
    setGate(el('div', { class: 'pv-card pv-center' },
        el('div', { class: 'pv-card-title' }, closed ? 'Sign-in closed' : 'Sign-in problem'),
        el('div', { class: 'pv-card-sub' }, message),
        el('button', { class: 'pv-btn pv-btn-primary', onclick: () => void run() }, 'Sign in')));
}

function onAuthenticated() {
    // Hide the gate FIRST: even if transport install or dsh boot throws, the
    // page must never stay stuck behind the auth surface.
    hideGate();
    mountChrome();
    // Hand the sealed transport to dsh and boot it. The patched apps/web
    // main.ts reads __PRIVASYS_SEALED__ when its connection plugin applies.
    /** @type {any} */ (window).__PRIVASYS_SEALED__ = sealed;
    // Install the sealed transport BEFORE boot: dsh's connection plugin reads
    // window.__DSH_TRANSPORT__ when it applies, and its event client opens the
    // mux WebSocket during boot — both must already be sealed-routed.
    try {
        installSealedTransport(sealed);
    } catch (err) {
        console.error('[privasys-shell] sealed transport install threw:', err);
    }
    const boot = /** @type {any} */ (window).__PRIVASYS_BOOT__;
    if (typeof boot === 'function') {
        try {
            boot();
        } catch (err) {
            console.error('[privasys-shell] dsh boot threw:', err);
        }
    } else {
        console.warn('[privasys-shell] __PRIVASYS_BOOT__ not installed; dsh entry missing?');
    }
}

// --- sealed transport injection (dsh alpha @Remote gateway) ----------------
// The alpha removed the legacy APIProxy (AbstractApiClient) that our old
// overlay subclassed. dsh now reads a single global seam,
// `window.__DSH_TRANSPORT__ = { fetch }`, for all unary /api RPC, and — when no
// in-process stream carrier is provided — opens ONE multiplexed WebSocket at
// `/api/remote.mux` for events + every @Remote stream. We inject both here
// from our vanilla shell, so NO dsh transport source is patched:
//   * unary /api  -> sealed.request() over sealed HTTP (FIFO-ordered: the relay
//     enforces a strict monotonic c2s HTTP counter, so concurrent frames must
//     arrive in order — same fix as before, now here instead of in a TS carrier)
//   * /api/remote.mux -> sealed.openWebSocket(); we intercept ONLY that URL and
//     adapt the SDK's SealedWebSocket (ready/onMessage/onClose callbacks, binary
//     sealed frames) to the native WebSocket API dsh's mux client expects
//     (addEventListener + string message data). dsh's mux code is untouched.
const SEALED_WS_SUBPROTOCOL = 'privasys.sealed.v1';
const MUX_PATH = '/api/remote.mux';
// The proxy's own sealed event sockets (capability_api.go): /privasys/*/events.
const HARNESS_EVENTS_PREFIX = '/privasys/';

// The session the transport uses. Replaced in place when an expired session
// is renewed (recoverSession), so dsh's own reconnects land on the new one.
const transport = { session: /** @type {any} */ (null) };

function installSealedTransport(session) {
    transport.session = session;
    // Unary RPC: FIFO chain so sealed HTTP frames reach the relay in counter
    // order (out-of-order = 401 replay -> "connection lost" storm).
    let chain = Promise.resolve();
    const enqueue = (task) => {
        const run = chain.then(task, task);
        chain = run.then(noop, noop);
        return run;
    };
    async function sealedFetch(input, init) {
        const url = typeof input === 'string' ? new URL(input, location.origin) : input;
        const path = url.pathname + url.search;
        const method = (init && init.method ? init.method : 'GET').toUpperCase();
        const body = init && init.body != null ? init.body : undefined;
        const opts = init && init.signal ? { signal: init.signal } : undefined;
        let r;
        try {
            r = await enqueue(() => transport.session.request(method, path, body, opts));
        } catch (err) {
            // A rejected request (not an HTTP status) means the sealed channel
            // itself is gone.
            if (!(init && init.signal && init.signal.aborted)) socketFailed();
            throw err;
        }
        return new Response(/** @type {any} */ (r.body), { status: r.status });
    }
    // ownsHost: dsh gates its privileged surface (writable Settings, the
    // Models/provider directory, native-open capabilities) on "is the browser
    // on the operator's own machine" — loopback, by default. Our page owns the
    // Host in exactly the sense the flag documents: dsh runs inside the
    // enclave, reachable ONLY through this authenticated sealed session, so
    // the loopback stand-in is vacuous. Without it the Models settings page
    // fails with "settings are unavailable in this browser".
    // ⚠ Known open question (multi-tenancy): every authenticated Privasys user
    // currently reaches the SAME dsh host; per-user isolation is a separate
    // workstream and this flag is correct only while the harness is
    // effectively single-tenant.
    /** @type {any} */ (window).__DSH_TRANSPORT__ = { fetch: sealedFetch, ownsHost: true };

    // Event downlink: route ONLY the mux socket through the sealed session.
    // Everything else (incl. the SDK's own sealed socket, which carries the
    // sealed subprotocol) falls through to the native WebSocket, so we never
    // recurse into our own interception.
    const NativeWebSocket = window.WebSocket;
    function isMux(u) {
        try { return new URL(u, location.origin).pathname === MUX_PATH; } catch { return false; }
    }
    function carriesSealedProto(protocols) {
        if (!protocols) return false;
        const arr = Array.isArray(protocols) ? protocols : [protocols];
        return arr.indexOf(SEALED_WS_SUBPROTOCOL) !== -1;
    }
    // The harness's own event sockets ride the same sealed carrier: a socket
    // is the one channel the relay does not serialise, so a push that must
    // wait for the wallet never sits in the unary FIFO in front of dsh's
    // calls (a request the proxy held for 25 s made every workspace action
    // queue behind it, 2026-09-18).
    function isHarnessEvents(u) {
        try {
            const p = new URL(u, location.origin).pathname;
            return p.startsWith(HARNESS_EVENTS_PREFIX) && p.endsWith('/events');
        } catch { return false; }
    }
    function InterceptingWebSocket(url, protocols) {
        if (isMux(url) && !carriesSealedProto(protocols)) {
            return new SealedWebSocketAdapter(transport.session, MUX_PATH);
        }
        if (isHarnessEvents(url) && !carriesSealedProto(protocols)) {
            const u = new URL(url, location.origin);
            return new SealedWebSocketAdapter(transport.session, u.pathname + u.search);
        }
        return new NativeWebSocket(url, protocols);
    }
    InterceptingWebSocket.prototype = NativeWebSocket.prototype;
    for (const k of ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED']) {
        InterceptingWebSocket[k] = NativeWebSocket[k];
    }
    /** @type {any} */ (window).WebSocket = InterceptingWebSocket;
}

// Transport health, across every sealed socket this page opens.
//
// The sealed session slides: the relay ends it after a quarter of an hour
// without traffic, which is what a tab left alone runs into. From then on
// every socket dsh opens fails before it opens, and dsh's carrier opens the
// next one at once. Failures before open are paced (exponential, up to a
// minute), each adapter lets go of everything once it is over, and after a
// few in a row the session is treated as ended: the conversation is hidden
// behind the gate, and the session is renewed.
//
// Renewal is silent when it can be (the privasys.id session is still valid
// and the enclave accepts the stored voucher): the transport takes the new
// session in place and dsh reconnects on its own. When it cannot (Firefox's
// partitioned storage refuses a background resume, or the sign-in itself
// expired) the gate asks for one click, and the click runs the full connect,
// which may put an approval on the phone. Never automatically while the page
// is hidden: that would send a wallet request to a phone nobody is watching.
const transportHealth = { failures: 0 };
const SOCKET_OPEN_TIMEOUT_MS = 20000;
const LOST_AFTER_FAILURES = 4;
let recovering = false;

function socketOpened() {
    transportHealth.failures = 0;
}

/** Count a socket that never opened; returns how long to wait before saying so. */
function socketFailed() {
    transportHealth.failures++;
    if (transportHealth.failures >= LOST_AFTER_FAILURES) void recoverSession();
    return Math.min(60000, 500 * 2 ** Math.min(transportHealth.failures - 1, 7));
}

/** Put a renewed session in place and show the page again. */
function sessionRenewed(session) {
    sealed = session;
    transport.session = session;
    /** @type {any} */ (window).__PRIVASYS_SEALED__ = session;
    transportHealth.failures = 0;
    recovering = false;
    hideGate();
}

async function recoverSession() {
    if (recovering || !frame) return;
    recovering = true;
    // The session has ended: the conversation is no longer shown.
    setGate(spinnerCard('Reconnecting…', 'Your secure session with the harness ended. Restoring it.'));
    if (document.hidden) {
        await new Promise((resolve) => {
            const onVisible = () => {
                if (!document.hidden) {
                    document.removeEventListener('visibilitychange', onVisible);
                    resolve(undefined);
                }
            };
            document.addEventListener('visibilitychange', onVisible);
        });
    }
    try {
        // Silent: a new sealed channel from the stored voucher, no ceremony.
        if (typeof frame.destroySealedIframe === 'function') frame.destroySealedIframe();
        const session = await frame.resumeSession();
        console.log('[privasys-shell] sealed session renewed');
        sessionRenewed(session);
    } catch (err) {
        console.warn('[privasys-shell] silent renewal failed; asking for a click:', msg(err));
        showSessionEnded();
    }
}

function showSessionEnded() {
    setGate(el('div', { class: 'pv-card pv-center' },
        el('div', { class: 'pv-card-title' }, 'Your secure session ended'),
        el('div', { class: 'pv-card-sub' },
            'The encrypted session with the harness expired while the page was not in use. ' +
            'Reconnect to carry on where you left off; your phone may ask you to approve it.'),
        el('button', { class: 'pv-btn pv-btn-primary', onclick: () => void reconnect() }, 'Reconnect')));
}

/** The click: the full connect, which the SDK draws in the gate if it needs to. */
async function reconnect() {
    gate.replaceChildren();
    gate.classList.remove('pv-hidden');
    try {
        const res = await frame.connect();
        if (!res || !res.session) throw new Error('no sealed session');
        sessionRenewed(res.session);
    } catch (err) {
        console.warn('[privasys-shell] reconnect failed:', msg(err));
        // A different account or a refused approval: start again cleanly.
        location.reload();
    }
}

// Adapts an SDK SealedWebSocket to the browser WebSocket surface dsh's mux
// client uses: addEventListener('open'|'message'|'close'|'error'), readyState
// vs WebSocket.OPEN, send(string), close(code,reason). dsh requires text
// message data, so inbound sealed bytes are UTF-8 decoded to a string.
//
// The SDK keeps the callbacks registered here for as long as it keeps the
// socket, which for a dead session is for ever. They therefore reach the
// adapter only through `ref`, cut when the socket is over, so what the SDK
// retains is an empty box and not the adapter with every listener dsh hung
// on it.
class SealedWebSocketAdapter extends EventTarget {
    constructor(session, path) {
        super();
        this.CONNECTING = 0; this.OPEN = 1; this.CLOSING = 2; this.CLOSED = 3;
        this.readyState = 0;
        this.binaryType = 'blob';
        this._decoder = new TextDecoder();
        this._done = false;
        const ref = { a: this };
        this._ref = ref;
        try {
            this._sws = session.openWebSocket(path);
        } catch (err) {
            this._sws = null;
            this._finish(1006, msg(err), false);
            return;
        }
        this._timer = setTimeout(() => {
            const a = ref.a;
            if (a && a.readyState === 0) {
                try { a._sws && a._sws.close(4008, 'open timed out'); } catch { /* ignore */ }
                a._finish(1006, 'the sealed socket did not open in time', false);
            }
        }, SOCKET_OPEN_TIMEOUT_MS);
        this._sws.ready.then(
            () => {
                const a = ref.a;
                if (!a || a._done) return;
                clearTimeout(a._timer);
                a.readyState = 1;
                socketOpened();
                a.dispatchEvent(new Event('open'));
            },
            (err) => { const a = ref.a; if (a) a._finish(1006, msg(err), false); }
        );
        this._unsub = this._sws.onMessage((bytes) => {
            const a = ref.a;
            if (a && a.readyState === 1) {
                a.dispatchEvent(new MessageEvent('message', { data: a._decoder.decode(bytes) }));
            }
        });
        this._sws.onClose((info) => {
            const a = ref.a;
            if (a) a._finish(info.code, info.reason, info.wasClean);
        });
        this._sws.onError(() => {
            const a = ref.a;
            if (a && !a._done) a.dispatchEvent(new Event('error'));
        });
    }
    send(data) {
        // dsh sends JSON text; the SDK seals it into a binary frame.
        if (this.readyState !== 1 || !this._sws) throw new Error('the sealed socket is not open');
        this._sws.send(data);
    }
    close(code, reason) {
        if (this._done) return;
        try { if (this._sws) this._sws.close(code, reason); } catch { /* ignore */ }
        // A native socket reports its close on its own; this one waits for the
        // sealed frame to confirm, which a dead session never does. Say it now.
        this._finish(typeof code === 'number' ? code : 1000, reason || '', true);
    }
    /** The socket is over: say so once, and let go of everything. */
    _finish(code, reason, wasClean) {
        if (this._done) return;
        this._done = true;
        const opened = this.readyState === 1;
        this.readyState = 3;
        clearTimeout(this._timer);
        try { if (this._unsub) this._unsub(); } catch { /* ignore */ }
        this._unsub = null;
        this._sws = null;
        if (this._ref) this._ref.a = null;
        // A socket that never opened is paced, so a dead session costs one
        // attempt a minute at worst rather than a tight loop.
        const delay = opened ? 0 : socketFailed();
        const fire = () => {
            if (!opened && !wasClean) this.dispatchEvent(new Event('error'));
            this.dispatchEvent(new CloseEvent('close', { code, reason, wasClean }));
        };
        if (delay > 0) setTimeout(fire, delay);
        else queueMicrotask(fire);
    }
}
function msg(err) { return String((err && err.message) || err || 'error'); }
function noop() { /* keep the FIFO chain alive regardless of task outcome */ }

// The Secure Hardware Attestation and User (Sign out) controls live in dsh's
// left sidebar foot (overlay/brand/PrivasysAttestation.tsx + PrivasysRows.tsx)
// — rendered by dsh itself on the SHARED @privasys/attestation-view component.
// The shell owns only auth, transport, and logout; chrome stays empty.
function mountChrome() {
    chrome.replaceChildren();
    chrome.classList.remove('pv-hidden');
}

// Clear the sealed session and re-gate. clearSession() tells the SDK's session
// iframe to forget stored credentials so the reload shows the ceremony again
// instead of silently restoring. Best-effort: even if clearSession is missing
// or throws, we still reload into the gate.
let loggingOut = false;
async function logout() {
    if (loggingOut) return;
    loggingOut = true;
    try {
        if (frame && typeof frame.clearSession === 'function') await frame.clearSession();
    } catch (err) {
        console.warn('[privasys-shell] clearSession failed; reloading anyway:', err);
    }
    try {
        delete (/** @type {any} */ (window)).__PRIVASYS_SEALED__;
    } catch { /* ignore */ }
    sealed = null;
    location.reload();
}

// --- small view helpers ----------------------------------------------------
function spinnerCard(title, sub) {
    return el('div', { class: 'pv-card pv-center' },
        el('div', { class: 'pv-spinner' }),
        el('div', { class: 'pv-card-title' }, title),
        sub ? el('div', { class: 'pv-card-sub' }, sub) : null);
}
// Publish shell actions + attestation config for the React sidebar rows and
// the trajectory Attestation tab (overlay/brand + overlay/trajectory). The
// "Secure Hardware Attestation" row renders the SHARED @privasys/
// attestation-view against these; getTokenForAudience mints the
// attestation-server-audience token through the sealed AuthFrame (the same
// pattern chat.privasys.org uses).
// The signed-in user's Display Name, set once connect() resolves (the React
// rows mount only after the post-auth dsh boot, so it is ready by then).
let userDisplayName;

/** Decode one claim from a JWT payload (presentation only, no verification). */
function claimFromJwt(jwt, claim) {
    try {
        const b64 = String(jwt).split('.')[1].replace(/-/g, '+').replace(/_/g, '/');
        const value = JSON.parse(atob(b64))[claim];
        return typeof value === 'string' && value.trim() ? value.trim() : undefined;
    } catch {
        return undefined;
    }
}

/** @type {any} */ (window).__PRIVASYS_SHELL__ = {
    logout: () => void logout(),
    userName: () => userDisplayName,
    attestUrl: CFG.attestBase + '/api/v1/apps/' + CFG.appId + '/attest',
    verifyQuoteUrl: CFG.verifyQuoteUrl,
    getTokenForAudience: async (audience) => {
        if (!frame || typeof frame.getTokenForAudience !== 'function') {
            throw new Error('auth frame not ready');
        }
        // The mint runs through the persistent session iframe; prime it first
        // ("no active session iframe; call getSession() first").
        await frame.getSession().catch(() => null);
        return frame.getTokenForAudience(audience);
    }
};

// --- go --------------------------------------------------------------------
run();
