// Apply the Privasys attested-harness web overlay onto a vendored dsh tree.
//
// This is the sanctioned patch-queue divergence (D8: extend-don't-fork), rebased
// for dsh v0.1.5-rc.2 (every anchor of the 0.1.5-alpha.2 rebase still holds:
// between alpha.2 and rc.2 only the DeepSeek catalogue moved, and not at our
// anchor; the alpha.2 rebase found the same for alpha.1:
// upstream left the webserver, connection, presets and bundle base untouched;
// that rebase covered the persona prefix/suffix split, and the one before it the
// @Remote gateway and the removal of APIProxy/AbstractApiClient). It:
//   1. copies NEW / replaced files (no rebase conflicts):
//        - apps/web/src/main.ts                    (gated boot)
//        - apps/web/index.html                     (shell script + roots)
//        - apps/web/public/privasys/*              (shell bundle + SDK IIFE)
//        - ui-brand-official Brand.tsx             (Privasys mark/name in the slots)
//        - favicon.svg                             (Privasys logo)
//   2. applies THREE anchored edits (fail the build if an anchor moved under a
//      re-pin — that is the signal to rebase the patch, never a silent skip):
//        (a) gateway mux server: accept binary WebSocket frames. The sealed relay
//            (enclave-os sessionrelay/websocket.go) writes client->app frames as
//            binary; dsh's server otherwise closes them 1003. rawText() already
//            decodes bytes as UTF-8, so we accept both opcodes.
//        (b) connection requestRejection: defer the alpha's browser launch-token
//            401 guard to the attested ingress. On the confidential platform dsh
//            is reachable only via the enclave manager -> in-TCB egress-proxy
//            (trusted Host); the sealed session the manager already terminated IS
//            the auth, and the sealed relay cannot carry dsh's per-process cookie.
//        (b') webserver: with DSH_INGRESS_TOKEN set, refuse every request and
//            upgrade that does not carry it. One dsh per user (WS5) puts several
//            users' processes on one loopback; only the proxy holds each token.
//
// The sealed TRANSPORT is injected at runtime by the vanilla shell
// (privasys-shell.js: window.__DSH_TRANSPORT__.fetch + a mux WebSocket adapter),
// so NO dsh transport source is patched (the old privasys-api-client.ts,
// client/index.ts selector, tsconfig entry and 426 fence edits are all gone).
// No package.json is touched, so the frozen lockfile still holds.
//
// Usage: node apply-overlay.mjs <dsh-root>
import { readFileSync, writeFileSync, copyFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const dsh = process.argv[2]
if (!dsh) {
  console.error('usage: node apply-overlay.mjs <dsh-root>')
  process.exit(1)
}

function edit(rel, transforms) {
  const path = join(dsh, rel)
  let src = readFileSync(path, 'utf8')
  for (const [label, find, replace] of transforms) {
    if (!src.includes(find)) {
      throw new Error(`overlay anchor MISSING in ${rel}: "${label}". Upstream changed — rebase the patch.`)
    }
    src = src.replace(find, replace)
  }
  writeFileSync(path, src)
  console.log(`[overlay] patched ${rel}`)
}

function put(rel, from) {
  const dest = join(dsh, rel)
  mkdirSync(dirname(dest), { recursive: true })
  copyFileSync(join(here, from), dest)
  console.log(`[overlay] wrote ${rel}`)
}

// --- 1. new / replaced files ------------------------------------------------
put('apps/web/src/main.ts', 'overlay/apps-web/main.ts')
put('apps/web/index.html', 'overlay/apps-web/index.html')
// Shell assets served as static public files by vite (public/ -> dist/).
put('apps/web/public/privasys/privasys-shell.js', 'privasys-shell.js')
put('apps/web/public/privasys/privasys-shell.css', 'privasys-shell.css')
put('apps/web/public/privasys/privasys-auth-client.iife.js', 'vendor/privasys-auth-client.iife.js')
put('apps/web/public/privasys/privasys-logo.mini.svg', 'vendor/privasys-logo.mini.svg')
put('apps/web/public/privasys/privasys-harness-logo.svg', 'vendor/privasys-harness-logo.svg')
// Rebrand at the SOURCE: FishLogo/BrandWordmark in ui-primitives carry every
// brand surface (the sidebar mark, the wordmark, and the conversation hero's
// animated fallback, which composes its own svg from FISH_LOGO_PATH) — so the
// stock ui-brand-official Brand.tsx needs no override on the alpha. Its
// index.ts IS overridden: it keeps upstream's brand registrations and adds the
// two Privasys sidebar-foot rows (Attestation "Verified" + User/Sign out) into
// the sidebar.footer.action list slot, next to Settings.
put('packages/client/ui-primitives/src/FishLogo.tsx', 'overlay/brand/FishLogo.tsx')
put('packages/client/ui-primitives/src/BrandWordmark.tsx', 'overlay/brand/BrandWordmark.tsx')
put('packages/client/ui-brand-official/src/client/index.ts', 'overlay/brand/index.ts')
put('packages/client/ui-brand-official/src/client/PrivasysRows.tsx', 'overlay/brand/PrivasysRows.tsx')
// The foot rows are dsh's own foot control (Settings trigger / Cordis badge
// geometry, dsh icons, StateDot status, Menu and Modal primitives) — one
// shared row component and one CSS module. ui-brand-official ships no CSS
// modules of its own, so the ambient module declaration rides along.
put('packages/client/ui-brand-official/src/client/PrivasysFootRow.tsx', 'overlay/brand/PrivasysFootRow.tsx')
put('packages/client/ui-brand-official/src/client/PrivasysFoot.module.css', 'overlay/brand/PrivasysFoot.module.css')
put('packages/client/ui-brand-official/src/css-modules.d.ts', 'overlay/brand/css-modules.d.ts')
put('apps/web/public/favicon.svg', 'vendor/privasys-logo.mini.svg')

// --- 2a. gateway mux server: accept binary frames ---------------------------
edit('packages/api/gateway/src/stream-server.ts', [
  [
    'mux server binary-frame acceptance',
    `      this.socket.on('message', (data, isBinary) => {\n` +
      `        if (isBinary) {\n` +
      `          this.socket.close(1003, 'text messages required')\n` +
      `          return\n` +
      `        }\n` +
      `        try {\n` +
      `          this.receive(rawText(data))\n` +
      `        } catch {\n` +
      `          this.socket.close(1008, 'invalid Remote stream request')\n` +
      `        }\n` +
      `      })`,
    `      this.socket.on('message', (data, _isBinary) => {\n` +
      `        // Privasys: the sealed relay (enclave-os sessionrelay/websocket.go)\n` +
      `        // writes client->app frames as binary; rawText() decodes\n` +
      `        // Buffer/ArrayBuffer as UTF-8, so accept both opcodes instead of\n` +
      `        // rejecting binary.\n` +
      `        try {\n` +
      `          this.receive(rawText(data))\n` +
      `        } catch {\n` +
      `          this.socket.close(1008, 'invalid Remote stream request')\n` +
      `        }\n` +
      `      })`,
  ],
])

// --- 2b. connection: defer the alpha's browser launch-token guard to the
//         attested ingress, for BOTH the /api fence and the index (GET /).
//         On the confidential platform dsh is reachable ONLY via the enclave
//         manager -> in-TCB egress-proxy (loopback), which forces a trusted
//         Host; the sealed CBOR-AES-GCM session the manager already terminated
//         IS the authentication, and the sealed relay cannot carry dsh's
//         per-process launch-token cookie. So a request that clears the
//         trusted-host fence is authenticated. (browserAuth still owns
//         authenticatedUrl + the token/cookie machinery for direct use.)
edit('packages/client/connection/src/rpc-host.ts', [
  [
    'requestRejection launch-token deferral',
    `  requestRejection(request: ConnectionTrustRequest): ConnectionRequestRejection {\n` +
      `    if (!isTrustedApiRequest(request, this.trustedHosts)) return 403\n` +
      `    return this.browserAuth.isAuthenticated(request) ? undefined : 401\n` +
      `  }`,
    `  requestRejection(request: ConnectionTrustRequest): ConnectionRequestRejection {\n` +
      `    if (!isTrustedApiRequest(request, this.trustedHosts)) return 403\n` +
      `    // Privasys: trusted Host == attested ingress == authenticated (see note above).\n` +
      `    return undefined\n` +
      `  }`,
  ],
  [
    'authorizeIndex launch-token deferral',
    `  authorizeIndex(request: ConnectionIndexRequest, response: ConnectionIndexResponse): boolean {\n` +
      `    return this.browserAuth.authorizeIndex(request, response)\n` +
      `  }`,
    `  authorizeIndex(request: ConnectionIndexRequest, response: ConnectionIndexResponse): boolean {\n` +
      `    // Privasys: a trusted-host index request came through the measured\n` +
      `    // ingress, so serve the SPA without the launch-token cookie the sealed\n` +
      `    // relay cannot carry (ConnectionIndexRequest extends ConnectionTrustRequest).\n` +
      `    if (isTrustedApiRequest(request, this.trustedHosts)) return true\n` +
      `    return this.browserAuth.authorizeIndex(request, response)\n` +
      `  }`,
  ],
])

// --- 2b'. webserver: one user per dsh process, one token per process.
//          Under the per-user proxy (WS5) every worker's dsh listens on a
//          loopback port that every process in the container can reach,
//          including the other users' sandboxed shells. 2b made "trusted Host"
//          equal "authenticated", which is right for the relay hop but not for
//          a loopback neighbour. With DSH_INGRESS_TOKEN set (the proxy mints
//          one per worker), the webserver refuses any request or upgrade that
//          does not carry it, before routing. Unset (legacy single-user
//          layout) nothing changes.
// 2c. Replayed time context. dsh's time-context plugin samples the clock at
//     every step and injects it as a user message, so the session record
//     carries the sampled time. On a replay the measured proxy normalises the
//     model leg to the RECORDED text (the model sees the original time), but
//     the record still showed a fresh clock, which reads as a broken replay
//     (2026-09-10, three times). The plugin now asks the proxy for the pinned
//     text of the next step before sampling, and injects that verbatim: the
//     record and the model leg agree. Any failure falls back to a fresh clock.
edit('packages/context/time-context/src/index.ts', [
  [
    'time-context: privasys pinned-time helper',
    `export const name = 'time-context'\n`,
    `export const name = 'time-context'\n` +
      `\n` +
      `// Privasys: an armed replay pins the time text of the next step (the\n` +
      `// measured proxy holds the recorded turn). Loopback only; the worker's own\n` +
      `// bearer names the user, so it can read nobody else's plan.\n` +
      `async function privasysPinnedTimeContext(session: string, signal: AbortSignal): Promise<string | undefined> {\n` +
      `  const base = process.env.DEEPSEEK_BASE_URL ?? ''\n` +
      `  const at = base.indexOf('/model/v1')\n` +
      `  const token = process.env.PRIVASYS_BEARER ?? ''\n` +
      `  if (at < 0 || token === '') return undefined\n` +
      `  const controller = new AbortController()\n` +
      `  const timer = setTimeout(() => { controller.abort() }, 2000)\n` +
      `  const stop = (): void => { controller.abort() }\n` +
      `  signal.addEventListener('abort', stop, { once: true })\n` +
      `  try {\n` +
      `    const url = base.slice(0, at) + '/privasys/replay/time-context?session=' + encodeURIComponent(session)\n` +
      `    const res = await fetch(url, { headers: { authorization: 'Bearer ' + token }, signal: controller.signal })\n` +
      `    if (!res.ok) return undefined\n` +
      `    const data = await res.json() as { pinned?: boolean; text?: string }\n` +
      `    return data.pinned === true && typeof data.text === 'string' && data.text !== '' ? data.text : undefined\n` +
      `  } catch {\n` +
      `    return undefined\n` +
      `  } finally {\n` +
      `    clearTimeout(timer)\n` +
      `    signal.removeEventListener('abort', stop)\n` +
      `  }\n` +
      `}\n`,
  ],
  [
    'time-context: inject the pinned text on a replay',
    `    const text = renderText(\n` +
      `      now,\n`,
    `    const pinned = await privasysPinnedTimeContext(agent.session.id, signal)\n` +
      `    const text = pinned ?? renderText(\n` +
      `      now,\n`,
  ],
])

edit('packages/host/webserver/src/index.ts', [
  [
    'ingress token helper (after injections import)',
    `import { renderIndexInjections, type IndexInjection } from './injections.ts'\n`,
    `import { renderIndexInjections, type IndexInjection } from './injections.ts'\n` +
      `import { timingSafeEqual } from 'node:crypto'\n` +
      `\n` +
      `// Privasys: the token the measured proxy presents on every request when\n` +
      `// this process serves one user among several (see createServer below).\n` +
      `const PRIVASYS_INGRESS_TOKEN = process.env.DSH_INGRESS_TOKEN ?? ''\n` +
      `function privasysIngressAdmits(req: IncomingMessage): boolean {\n` +
      `  if (PRIVASYS_INGRESS_TOKEN === '') return true\n` +
      `  const value = req.headers['x-privasys-ingress-token']\n` +
      `  if (typeof value !== 'string' || value.length !== PRIVASYS_INGRESS_TOKEN.length) return false\n` +
      `  return timingSafeEqual(Buffer.from(value), Buffer.from(PRIVASYS_INGRESS_TOKEN))\n` +
      `}\n`,
  ],
  [
    'ingress token gate on requests',
    `    this.server = createServer((req, res) => {\n` +
      `      const next = (): void => {\n`,
    `    this.server = createServer((req, res) => {\n` +
      `      // Privasys: only the proxy that started this process may use it.\n` +
      `      if (!privasysIngressAdmits(req)) {\n` +
      `        res.writeHead(401)\n` +
      `        res.end('unauthorized')\n` +
      `        return\n` +
      `      }\n` +
      `      const next = (): void => {\n`,
  ],
  [
    'ingress token gate on upgrades',
    `    this.server.on('upgrade', (req, socket, head) => {\n` +
      `      const onError = (error: Error): void => {\n`,
    `    this.server.on('upgrade', (req, socket, head) => {\n` +
      `      // Privasys: same gate as the request path.\n` +
      `      if (!privasysIngressAdmits(req)) {\n` +
      `        socket.destroy()\n` +
      `        return\n` +
      `      }\n` +
      `      const onError = (error: Error): void => {\n`,
  ],
])

// --- 2c. agent presets: the attested MCP fleet replaces the built-in web tool
// Each agent preset mounts `tool-web` in its OWN composition tree, which
// profile patches do not reach — the row would wait on the missing `web`
// service forever and the whole preset fails to mount. In its place the three
// attested platform tools are composed HERE, in the preset, not in the
// deployment profile: preset rows are what the Settings "Plugins" inventory
// lists as session plugins (the deployment plane is collapsed under it), and
// the preset layer is where per-preset activation lives — first-class,
// visible, toggleable rows instead of invisible plumbing. mcp-client reserves
// its serverName per standing preset scope (scopeOf(ctx)), so all three
// presets can mount the same fleet without a namespace collision, and each
// preset mounts ONCE per process — three shim connections per preset, local
// and stateless.
const TOOL_WEB_BLOCK =
  `# The \`web\` service and its search provider stay in the host composition; only\n` +
  `# the model-facing tool is per-session.\n` +
  `- id: tool-web\n` +
  `  name: '@deepseek-ai/dsh-tool-web'\n` +
  `  config:\n` +
  `    fetch: true\n` +
  `    searchTimeoutMs: 60000\n`
const mcpFleetRow = (id, server) =>
  `- id: ${id}\n` +
  `  name: '@deepseek-ai/dsh-mcp-client'\n` +
  `  config:\n` +
  `    transport: streamable-http\n` +
  `    serverName: ${server}\n` +
  `    url: http://127.0.0.1:9411/tool/${server}/mcp\n` +
  `    headers:\n` +
  `      authorization: !!js "'Bearer ' + (process.env.PRIVASYS_BEARER || '')"\n` +
  `    failOnStartupError: false\n`
const MCP_FLEET_ROWS =
  `# Privasys: the built-in web tool is replaced by the attested MCP fleet —\n` +
  `# each row is one attested platform tool app behind the in-TCB egress proxy\n` +
  `# (mutual RA-TLS, DepSet-gated). failOnStartupError: false so an unreachable\n` +
  `# tool app degrades that tool, never the whole preset.\n` +
  mcpFleetRow('web-search', 'web_search') + `\n` +
  mcpFleetRow('web-reader', 'web_reader') + `\n` +
  mcpFleetRow('drive', 'drive') + `\n` +
  // The mail connector. One image serves both fleets and this overlay runs at
  // BUILD time, so the row is mounted on both; only dev has a host for it
  // (entrypoint.sh appends one), and on prod the row degrades to nothing with
  // a logged catalogue failure until a prod connector exists. That is the
  // honest state of a tool one fleet does not have, and failOnStartupError
  // keeps it from touching the rest of the preset.
  mcpFleetRow('mail', 'mail')
for (const preset of ['standard', 'ptc', 'cordis']) {
  edit(`packages/preset/agent-presets/presets/${preset}/agent.cordis.yml`, [
    [`preset ${preset} attested-fleet swap`, TOOL_WEB_BLOCK, MCP_FLEET_ROWS],
  ])
}

// The three mcp-client rows all share one module name, so the Plugins
// inventory would render three identical "mcp-client" cards — show the row's
// entry id (the tool identity: web-search / web-reader / drive) instead.
edit('packages/client/ui-settings-plugin-inventory/src/client/PluginInventorySettingsTab.tsx', [
  [
    'mcp-client card title by entry id',
    `        <strong className={css.cardTitle} title={moduleName}>{moduleShortName(moduleName)}</strong>`,
    `        <strong className={css.cardTitle} title={moduleName}>{\n` +
      `          /* Privasys: mcp-client rows are distinguished by entry id (tool identity). */\n` +
      `          moduleName === '@deepseek-ai/dsh-mcp-client' && entryId !== null ? entryId : moduleShortName(moduleName)\n` +
      `        }</strong>`,
  ],
])

// --- 2d. cache_salt: partition the confidential backend's prefix cache ------
// vLLM behind Confidential AI supports a per-request `cache_salt`; dsh's
// designed seam is the deepseek-llm-api-extensions registry (extra top-level
// body fields, sessionId provided per request). Self-register in the registry's
// constructor so no extra plugin row or package is needed; the session id is
// stable per session and unique across sessions — exactly the salt contract.
edit('packages/llm/deepseek-llm-api-extensions/src/types.ts', [
  [
    'extension map cache_salt merge',
    `export interface DeepSeekLlmApiExtensionMap {}`,
    `export interface DeepSeekLlmApiExtensionMap {\n` +
      `  /** Privasys: per-session prefix-cache partition salt (vLLM cache_salt). */\n` +
      `  cache_salt: string\n` +
      `}`,
  ],
])
edit('packages/llm/deepseek-llm-api-extensions/src/index.ts', [
  [
    'registry constructor cache_salt registration',
    `  constructor(ctx: Context) {\n` +
      `    super(ctx, 'deepseekLlmApiExtensions')\n` +
      `  }`,
    `  constructor(ctx: Context) {\n` +
      `    super(ctx, 'deepseekLlmApiExtensions')\n` +
      `    // Privasys: partition the confidential backend's prefix cache per\n` +
      `    // session (vLLM cache_salt) so sessions never share cached prefixes.\n` +
      `    this.register('cache_salt', {\n` +
      `      prepare: request => request.sessionId === undefined\n` +
      `        ? undefined\n` +
      `        : { value: request.sessionId },\n` +
      `    })\n` +
      `  }`,
  ],
])

// --- 2e. preset personas: Privasys identity ---------------------------------
// Preset dsh-persona rows SHADOW the deployment persona (same section name in
// the agent scope), so the profile-level Privasys persona never shows in
// preset sessions — rewrite the preset texts themselves. Since dsh
// 0.1.3-alpha.2 the persona is split into a `prefix` (identity, rendered as
// deployment:persona-prefix) and a `suffix` (rendered after first-party
// guidance); the working-directory line lives in the suffix and is kept as
// upstream wrote it — only the identity prefix is rebranded.
const PRIVASYS_PERSONA_PREFIX =
  `      You are a coding agent of the Privasys Harness, powered by the {{model}} model running in a hardware-attested confidential enclave.`
for (const preset of ['standard', 'ptc']) {
  edit(`packages/preset/agent-presets/presets/${preset}/agent.cordis.yml`, [
    [
      `preset ${preset} persona rebrand`,
      `    prefix: >-\n` +
        `      You are a coding agent powered by the {{model}} model.`,
      `    prefix: >-\n` +
        PRIVASYS_PERSONA_PREFIX,
    ],
  ])
}
edit('packages/preset/agent-presets/presets/cordis/agent.cordis.yml', [
  [
    'preset cordis persona rebrand',
    `    prefix: |-\n` +
      `      You are a coding agent powered by the {{model}} model, running on the DeepSeek Harness.`,
    `    prefix: >-\n` +
      PRIVASYS_PERSONA_PREFIX,
  ],
])

// --- 2e0. Firefox: intrinsic-prototype detection is V8-specific -------------
// dsh 0.1.3's lossless-JSON validator proves a value's prototype is a realm's
// intrinsic Object.prototype by comparing the constructor's source text to the
// exact literal `function Object() { [native code] }`. That is V8's formatting.
// SpiderMonkey prints `function Object() {\n    [native code]\n}`, so on
// FIREFOX every plain object fails the check — every assistant-stream chunk is
// refused, the session event feed dies at connect, and the UI shows
// "Assistant stream raw chunk must be a lossless JSON object" for a chunk whose
// keys, values and prototype are all perfectly ordinary. Chromium is
// unaffected, which is why this looked like a data bug for a long time.
//
// Compare on whitespace-normalised source so V8, SpiderMonkey and JSC all
// satisfy it while a FORGED constructor (any body that is not the native
// marker) still fails. Upstream fix candidate: report and drop this patch.
edit('packages/util/values/src/index.ts', [
  [
    'engine-agnostic intrinsic constructor check',
    `    return constructor.name === name\n` +
      `      && constructor.prototype === prototype\n` +
      `      && Function.prototype.toString.call(constructor) === \`function \${name}() { [native code] }\``,
    `    // privasys: whitespace-normalised — SpiderMonkey and JSC format the\n` +
      `    // native marker with newlines and indentation, V8 does not.\n` +
      `    return constructor.name === name\n` +
      `      && constructor.prototype === prototype\n` +
      `      && Function.prototype.toString.call(constructor).replace(/\\s+/g, ' ')\n` +
      `        === \`function \${name}() { [native code] }\``,
  ],
])

// The same V8-only comparison is duplicated in three more copies of the
// helper (upstream discussion #5709 counts four sites in total). core/tools
// guards TOOL JSON SCHEMAS, which is why Firefox lost tool calls specifically
// while plain chat still rendered; the other two cover the host runner and the
// worker-thread runtime, which use their own intrinsic-capture style.
for (const rel of [
  'packages/core/tools/src/json-schema.ts',
  'packages/extensions/cordis-host-runner/src/guard.ts',
]) {
  edit(rel, [
    [
      'engine-agnostic intrinsic constructor check',
      `    return constructor.name === name\n` +
        `      && constructor.prototype === prototype\n` +
        `      && Function.prototype.toString.call(constructor) === \`function \${name}() { [native code] }\``,
      `    // privasys: whitespace-normalised (see values/src/index.ts).\n` +
        `    return constructor.name === name\n` +
        `      && constructor.prototype === prototype\n` +
        `      && Function.prototype.toString.call(constructor).replace(/\\s+/g, ' ')\n` +
        `        === \`function \${name}() { [native code] }\``,
    ],
  ])
}
edit('packages/code-runtime/code-runtime-worker-thread/src/worker-json.ts', [
  [
    'engine-agnostic intrinsic constructor check (worker)',
    `    return constructor.name === name\n` +
      `      && constructor.prototype === prototype\n` +
      `      && intrinsicReflectApply(intrinsicFunctionToString, constructor, []) === \`function \${name}() { [native code] }\``,
    `    // privasys: whitespace-normalised (see values/src/index.ts).\n` +
      `    return constructor.name === name\n` +
      `      && constructor.prototype === prototype\n` +
      `      && String(intrinsicReflectApply(intrinsicFunctionToString, constructor, [])).replace(/\\s+/g, ' ')\n` +
      `        === \`function \${name}() { [native code] }\``,
  ],
])

// --- 2e2. reasoning field compatibility (vLLM >= 0.28.0) ---------------------
// vLLM removed `reasoning_content` from chat output at 0.28.0 (#50624,
// after the rename in #33402): the engine now emits only `reasoning`, and
// input still accepts both. The failure is SILENT — a client reading
// delta.reasoning_content simply sees nothing and loses the entire chain of
// thought with no error. dsh's adapter reads only the old field, so accept
// either, which is exactly what our own chat front-end and Confidential
// AI's agent loop already do. Harmless against a DeepSeek-cloud endpoint,
// which keeps emitting reasoning_content.
edit('packages/llm/llm-deepseek/src/translate.ts', [
  [
    'reasoning delta field fallback',
    `      const reasoning = delta?.reasoning_content`,
    `      const reasoning = delta?.reasoning_content ?? delta?.reasoning`,
  ],
])
edit('packages/llm/llm-deepseek/src/types.ts', [
  [
    'WireDelta reasoning field',
    `  reasoning_content?: string | null\n  tool_calls?: WireToolCallDelta[]`,
    `  reasoning_content?: string | null\n  /** vLLM >= 0.28.0 emits the reasoning channel under this name. */\n  reasoning?: string | null\n  tool_calls?: WireToolCallDelta[]`,
  ],
])

// --- 2e1. bwrap: create a user namespace before the PID namespace -----------
// dsh's bwrap profile asks for --unshare-pid with no --unshare-user. That is
// fine on a developer laptop, where bwrap runs unprivileged and creates a user
// namespace of its own accord; it fails in our enclave container, where the
// process is uid 0 but the capability set has CAP_SYS_ADMIN dropped (measured
// 2026-09-06: CapEff=00000000a80425fb, the stock container set). bwrap then
// takes its privileged path, asks the kernel for a PID namespace directly, and
// gets:
//
//   bwrap: Creating new namespace failed: Operation not permitted
//
// which dsh reports only as the opaque "no sandbox backend is usable on this
// host", so every Bash call fails and the agent falls back to search tools.
//
// User namespaces themselves ARE permitted (`unshare -U true` exits 0,
// max_user_namespaces=59148), and adding --unshare-user does fix namespace
// creation — measured, it advances the failure to:
//
//   bwrap: Can't mount proc on /newroot/proc: Operation not permitted
//
// which is the second, separate restriction: the container's /proc is masked
// (the stock runtime hides /proc/kcore and friends), so the kernel does not
// consider it "fully visible" and refuses to mount a fresh procfs inside a
// nested user namespace. No argument ordering fixes that; only CAP_SYS_ADMIN
// or an unmasked /proc would, and neither belongs in an app container.
//
// So drop the PID namespace and the /proc mount as well. This costs nothing
// dsh actually promises: its sandbox vocabulary is explicitly file-effects
// only — "Network and process visibility are outside this vocabulary"
// (packages/sandbox/sandbox). The profile unshares PID anyway, which is a
// guarantee the surrounding API never makes and no caller can rely on. What
// remains — the read-only bind of /, the private /dev, the workspace-write
// bind and private /tmp — is the entire documented contract, intact.
//
// Trade-off, stated plainly: a sandboxed command can now see and signal other
// processes in this container. That is acceptable while a harness is
// single-user and the container is itself the tenancy boundary. It stops
// being acceptable when per-user worker processes share a container, which is
// exactly why the mutualisation plan puts execution isolation in per-user
// workers with their own uid rather than in dsh's sandbox
// (.operations/plans/harness-policies.md, v2).
//
// Upstreamable as a container-hosted-dsh fix (D8): unprivileged containers are
// a normal place to run an agent, and the PID namespace is not part of the
// sandbox's stated contract.
edit('packages/sandbox/sandbox-local/src/profiles.ts', [
  [
    'bwrap: user namespace, no pid namespace, no procfs mount',
    `  const args = ['--ro-bind', '/', '/', '--dev', '/dev', '--unshare-pid', '--proc', '/proc', '--die-with-parent']`,
    `  const args = ['--unshare-user', '--ro-bind', '/', '/', '--dev', '/dev', '--die-with-parent']`,
  ],
])

// --- 2e2. session export must ride the sealed transport ---------------------
// The Session log button downloads the session archive with the BROWSER's raw
// fetch (SessionLogDownloadController defaults its fetcher to global fetch and
// builds a same-origin URL from location.origin). Every other /api call in this
// deployment goes through the sealed channel, and the gateway refuses anything
// on /api that does not — so the button returned:
//
//   Export failed: HTTP 403   ("sealed-transport-required")
//
// The fence is right and the client simply predates it. The controller already
// takes an injectable fetcher for its tests, so hand it the sealed one when the
// shell has installed it (window.__DSH_TRANSPORT__.fetch, see
// web/privasys-shell.js) and fall back to global fetch off-platform, where
// there is no sealed session and the raw path is correct.
//
// The archive still streams from the host and is saved by the browser download
// manager exactly as upstream intends; only the carrier changes.
edit('packages/session-query/session-log-export/src/client/index.ts', [
  [
    'session export via the sealed transport',
    `  const controller = new SessionLogDownloadController()`,
    `  // Privasys: /api is sealed-transport only, and the browser's raw fetch is\n` +
    `  // refused by the gateway (403 sealed-transport-required). Use the sealed\n` +
    `  // carrier the shell installs; fall back to global fetch off-platform.\n` +
    `  const privasysTransport = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__\n` +
    `  const controller = new SessionLogDownloadController(\n` +
    `    privasysTransport?.fetch !== undefined\n` +
    `      ? (input, init) => privasysTransport.fetch!(input, init)\n` +
    `      : undefined,\n` +
    `  )`,
  ],
])

// --- 2f0. conversation hero headline ----------------------------------------
// The empty-session hero tagline is a locale literal ("Into the Unknown") —
// replace with the Privasys promise in both dictionaries.
edit('packages/client/ui-conversation/src/client/locales.ts', [
  [
    'zh hero headline',
    `  'hero.headline': '探索未至之境',`,
    `  'hero.headline': '你的数据。你的掌控。你的智能体。',`,
  ],
  [
    'en hero headline',
    `  'hero.headline': 'Into the Unknown',`,
    `  'hero.headline': 'Your data. Your control. Your agent.',`,
  ],
])

// --- 2f. trajectory "Attestation" detail tab --------------------------------
// The Inspect view's detail tabs are HARDCODED in TrajectoryTable.tsx (no slot
// exists — verified against the generated slot catalog), so the sixth tab
// needs three anchored edits + locale keys. The tab component itself is a new
// file (overlay/trajectory/PrivasysAttestationTab.tsx, react+fetch only).
put(
  'packages/client/ui-trajectory/src/client/PrivasysAttestationTab.tsx',
  'overlay/trajectory/PrivasysAttestationTab.tsx',
)
edit('packages/client/ui-trajectory/src/client/TrajectoryTable.tsx', [
  [
    'attestation tab import',
    `import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'`,
    `import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'\n` +
      `import { PrivasysAttestationTab } from './PrivasysAttestationTab.tsx'`,
  ],
  [
    'DetailTab union attestation',
    `  | 'timing'\n  | 'diff'\ntype RecordState`,
    `  | 'timing'\n  | 'diff'\n  | 'attestation'\ntype RecordState`,
  ],
  [
    'detailTabs tool branch attestation',
    `  return [\n` +
      `    { id: 'overview', labelKey: 'tab.summary' },\n` +
      `    ...(record.cell.inputDetail ? [{ id: 'input', labelKey: 'tab.payload' } as const] : []),\n` +
      `    ...(record.cell.outputDetail ? [{ id: 'output', labelKey: 'tab.result' } as const] : []),\n` +
      `    { id: 'schema', labelKey: 'tab.schema' },\n` +
      `    { id: 'timing', labelKey: 'tab.timing' },\n` +
      `  ]`,
    `  return [\n` +
      `    { id: 'overview', labelKey: 'tab.summary' },\n` +
      `    ...(record.cell.inputDetail ? [{ id: 'input', labelKey: 'tab.payload' } as const] : []),\n` +
      `    ...(record.cell.outputDetail ? [{ id: 'output', labelKey: 'tab.result' } as const] : []),\n` +
      `    { id: 'schema', labelKey: 'tab.schema' },\n` +
      `    { id: 'timing', labelKey: 'tab.timing' },\n` +
      `    { id: 'attestation', labelKey: 'tab.attestation' },\n` +
      `  ]`,
  ],
  [
    'attestation tab panel',
    `            {!promptSelected && selected !== undefined && activeTab === 'timing' && (\n` +
      `              <RecordTiming record={selected} t={t} />\n` +
      `            )}`,
    `            {!promptSelected && selected !== undefined && activeTab === 'timing' && (\n` +
      `              <RecordTiming record={selected} t={t} />\n` +
      `            )}\n` +
      `            {!promptSelected && selected !== undefined && activeTab === 'attestation' && (\n` +
      `              <PrivasysAttestationTab toolWireName={selected.cell.text} />\n` +
      `            )}`,
  ],
])
edit('packages/client/ui-trajectory/src/client/locales.ts', [
  [
    'zh tab.attestation',
    `  'tab.timing': '计时',`,
    `  'tab.timing': '计时',\n  'tab.attestation': '远程证明',`,
  ],
  [
    'en tab.attestation',
    `  'tab.timing': 'Timing',`,
    `  'tab.timing': 'Timing',\n  'tab.attestation': 'Attestation',`,
  ],
])

// --- 3. shared attestation view (vendored AS SOURCE) + its stylesheet -------
// The SAME @privasys/attestation-view every Privasys property renders
// (canonical: websites/libs/attestation-view), placed into ui-brand-official
// so the sidebar row compiles against it. The stylesheet is the lib's Tailwind
// utilities pre-extracted to a static file (theme + utilities layers ONLY — no
// preflight, so dsh's own styling is untouched).
for (const rel of [
  'index.ts', 'types.ts', 'use-attestation.ts',
  'components/attestation-result-view.tsx', 'components/composite-attestation-view.tsx',
  'components/attestation-connect.tsx', 'components/badge.tsx', 'components/field-row.tsx',
  'internal/use-copy.ts',
]) {
  put(`packages/client/ui-brand-official/src/client/attestation-view/${rel}`, `vendor/attestation-view/${rel}`)
  // Second copy inside ui-trajectory: the Attestation tab renders the same
  // full shared view, and a cross-package src import would break the strict
  // project-reference build — a duplicated vendored copy is the smaller evil.
  put(`packages/client/ui-trajectory/src/client/attestation-view/${rel}`, `vendor/attestation-view/${rel}`)
}
put('packages/client/ui-brand-official/src/client/PrivasysAttestation.tsx', 'overlay/brand/PrivasysAttestation.tsx')
// The policy section of that panel: what this harness PERMITS beside what it
// has actually REACHED. Two lines, never one — showing either half alone is
// how an honest product acquires a false badge. Reads the measured proxy's
// /privasys/policy and /privasys/egress-log, same-origin.
put('packages/client/ui-brand-official/src/client/PrivasysPolicy.tsx', 'overlay/brand/PrivasysPolicy.tsx')
// The storage row: where this user's sessions are kept, and the only place they
// can ask for them to live in their own Drive. A sidebar row rather than a
// modal, because the commitment is a NON-BLOCKING banner — nobody mid-task
// should be interrupted to decide about storage.
put('packages/client/ui-brand-official/src/client/PrivasysStorage.tsx', 'overlay/brand/PrivasysStorage.tsx')
put('apps/web/public/privasys/privasys-attestation.css', 'vendor/privasys-attestation.css')

// --- 2g. greenfield content: no DeepSeek-authored text reaches user or model
// (full audit in memory attested-harness-plan; consolidated change set below).

// (a) The blocking "Internal Testing Notice" + official-DeepSeek API-key
// onboarding dialogs. No config seam exists (apply() takes none); locale
// override is impossible (duplicate register throws). Remove the two slot
// registrations; `void` the now-unreferenced symbols so the strict build
// (noUnusedLocals) keeps compiling.
edit('packages/client/ui-settings-models/src/client/index.ts', [
  [
    'onboarding dialogs removal',
    `  ctx.slots.inject('settings.onboarding', () => ctx.slots.register({\n` +
      `    name: 'settings.onboarding',\n` +
      `    id: 'welcome-notice',\n` +
      `    order: -100,\n` +
      `    inject: welcomeInjected,\n` +
      `  }, WelcomeNotice))\n` +
      `  ctx.slots.inject('settings.onboarding', () => ctx.slots.register({\n` +
      `    name: 'settings.onboarding',\n` +
      `    id: 'deepseek-official',\n` +
      `    order: 0,\n` +
      `    inject: deepSeekOnboardingInjected,\n` +
      `  }, DeepSeekOnboardingDialog))`,
    `  // Privasys: the DeepSeek onboarding dialogs (the blocking "Internal\n` +
      `  // Testing Notice" and the official-API-key prompt) are not registered —\n` +
      `  // this deployment has its own identity and its provider is preconfigured.\n` +
      `  void welcomeInjected\n` +
      `  void deepSeekOnboardingInjected\n` +
      `  void WelcomeNotice\n` +
      `  void DeepSeekOnboardingDialog`,
  ],
])

// (b) The ?fixture demo transport: always bundled, reachable by URL query in
// production, serving DeepSeek-authored sample content. Disable outright.
edit('packages/client/connection/src/client/index.ts', [
  [
    'fixture transport removal',
    `  const fixture = pageLocation !== undefined && new URLSearchParams(pageLocation.search).has('fixture')\n` +
      `  const fixtureRpc = fixture ? createFixtureConnectionRpc() : undefined`,
    `  // Privasys: the ?fixture demo transport (DeepSeek-authored sample\n` +
      `  // content, reachable by query string) is disabled in this deployment.\n` +
      `  void pageLocation\n` +
      `  void createFixtureConnectionRpc\n` +
      `  const fixtureRpc = undefined`,
  ],
])

// (c) Model-visible sandbox-policy brand: "DSH file policy/sandbox" ->
// neutral "harness" (these strings ride the runtime-context snapshot into
// every prompt).
edit('packages/sandbox/sandbox-policy/src/index.ts', [
  [
    'sandbox policy read-only string',
    `      return 'Current DSH file policy: read-only. Any available operation enforced by the DSH file sandbox cannot modify files in the standing mode.`,
    `      return 'Current harness file policy: read-only. Any available operation enforced by the harness file sandbox cannot modify files in the standing mode.`,
  ],
  [
    'sandbox policy workspace-write string',
    '      return `Current DSH file policy: workspace-write. Any available operation enforced by the DSH file sandbox may modify files under the session workspace:',
    '      return `Current harness file policy: workspace-write. Any available operation enforced by the harness file sandbox may modify files under the session workspace:',
  ],
  [
    'sandbox policy full-access string',
    `      return 'Current DSH file policy: danger-full-access. The DSH file sandbox does not restrict file modifications by available operations.'`,
    `      return 'Current harness file policy: danger-full-access. The harness file sandbox does not restrict file modifications by available operations.'`,
  ],
])

// (d) The runtime-context provenance label rendered in the chat UI: a bare
// string literal, self-consistent (isOwned compares the same const), no other
// production matcher — rename to the neutral sibling style.
edit('packages/core/agent-loop/src/runtime-context.ts', [
  [
    'runtime-context source label',
    `const SOURCE = '@deepseek-ai/dsh-system-prompt'`,
    `const SOURCE = 'runtime-context'`,
  ],
])

// (f) The Settings "Models" provider card: the route stays `deepseek-official`
// (a wire identifier the presets and default-model rows reference), but the
// user-facing card names OUR attested provider, not DeepSeek. No config seam
// exists — displayName is a literal at the registration site.
edit('packages/llm/llm-deepseek/src/index.ts', [
  [
    'provider card rebrand',
    `    { provider: PROVIDER, displayName: 'DeepSeek', settingsNs: NS, settingsPath: [] },`,
    `    // Privasys: the settings card names the attested model behind this route.\n` +
      `    { provider: PROVIDER, displayName: 'Privasys Qwen 3.6', settingsNs: NS, settingsPath: [] },`,
  ],
])

// (e) Skill discovery: the dsh-* development skills live in the checkout's
// .agents/skills and are found through cwd-anchored default roots. Scope
// every preset to a directory the deployment owns (includeDefaultRoots:false
// is the load-bearing key — customSkillDirs alone would ADD, not replace).
const SKILL_ROW_STOCK =
  `- id: skill-filesystem\n` +
  `  name: '@deepseek-ai/dsh-skill-filesystem'\n` +
  `\n` +
  `- id: tool-skill`
const SKILL_ROW_PRIVASYS =
  `- id: skill-filesystem\n` +
  `  name: '@deepseek-ai/dsh-skill-filesystem'\n` +
  `  config:\n` +
  `    # Privasys: skills come ONLY from the deployment-owned directory on the\n` +
  `    # encrypted volume — never the dsh checkout's own development skills.\n` +
  `    includeDefaultRoots: false\n` +
  `    customSkillDirs:\n` +
  `      - /data/skills\n` +
  `    watch: false\n` +
  `\n` +
  `- id: tool-skill`
for (const preset of ['standard', 'ptc']) {
  edit(`packages/preset/agent-presets/presets/${preset}/agent.cordis.yml`, [
    [`preset ${preset} skill scoping`, SKILL_ROW_STOCK, SKILL_ROW_PRIVASYS],
  ])
}
edit('packages/preset/agent-presets/presets/cordis/agent.cordis.yml', [
  [
    'preset cordis skill scoping',
    `- id: skill-filesystem\n` +
      `  name: '@deepseek-ai/dsh-skill-filesystem'\n` +
      `  config:\n` +
      `    customSkillDirs:\n` +
      `      - !!js "process.getBuiltinModule('node:url').fileURLToPath(new URL('skills/', baseUrl))"`,
    `- id: skill-filesystem\n` +
      `  name: '@deepseek-ai/dsh-skill-filesystem'\n` +
      `  config:\n` +
      `    includeDefaultRoots: false\n` +
      `    customSkillDirs:\n` +
      `      - /data/skills\n` +
      `    watch: false`,
  ],
])

// --- 2h. reproducibility per reply + sampling pins ---------------------------
// Confidential AI ends every stream with a `data: {"reproducibility":…}`
// frame the proxy opts into and annotates (proxy sampling.go). dsh's
// translator drops frames without `choices`, so the block never reached the
// session log. Three anchored edits fold it into the assistant message's
// `usage` — the one merge-extensible field the loop persists verbatim and
// the client already reads for the Turn-usage pill — so the block is durable
// in the session log, mirrors to Drive, and shows in the trajectory's JSON
// tab with nothing else changed. The live write path validates only
// request/header and tool/result shapes (core/session surface.ts), and the
// Turn-usage fold reads named fields, so an extra key is inert everywhere
// but here.
edit('packages/llm/llm-deepseek/src/translate.ts', [
  [
    'repro: pending slot',
    `  let pendingUsage: TokenUsage | undefined\n`,
    `  let pendingUsage: TokenUsage | undefined\n` +
      `  // Privasys: Confidential AI's reproducibility trailer, kept beside usage.\n` +
      `  let pendingReproducibility: unknown\n`,
  ],
  [
    'repro: fold into usage at DONE',
    `      if (pendingUsage) yield { type: 'usage', usage: pendingUsage }`,
    `      if (pendingUsage) {\n` +
      `        yield {\n` +
      `          type: 'usage',\n` +
      `          usage: (pendingReproducibility === undefined\n` +
      `            ? pendingUsage\n` +
      `            : { ...pendingUsage, reproducibility: pendingReproducibility }) as TokenUsage,\n` +
      `        }\n` +
      `      }`,
  ],
  [
    'repro: capture the trailer',
    `    if (chunk.usage) pendingUsage = mapUsage(chunk.usage)`,
    `    if (chunk.usage) pendingUsage = mapUsage(chunk.usage)\n` +
      `    // Privasys: the trailer carries no choices and no usage; keep it.\n` +
      `    const reproducibility = (chunk as unknown as { reproducibility?: unknown }).reproducibility\n` +
      `    if (reproducibility !== undefined) pendingReproducibility = reproducibility`,
  ],
])

// The two client occupants live in ui-chat, which already depends on
// everything they need (sessions, the conversation slots, the stat-dialog
// skin, ui-primitives): a reproducibility action in the turn tail's
// assistant-actions strip, and a sampling chip in the composer's input.right
// seat. Registered from ui-chat's apply() via anchored edits, like the
// trajectory tab (§2f).
for (const rel of [
  'PrivasysReproducibility.tsx', 'PrivasysReproducibility.module.css',
  'PrivasysSampling.tsx', 'PrivasysSampling.module.css', 'privasys-fetch.ts',
]) {
  put(`packages/client/ui-chat/src/client/chat/${rel}`, `overlay/chat/${rel}`)
}
edit('packages/client/ui-chat/src/client/apply.ts', [
  [
    // cordis hands a plugin only the services it declares: the replay reads
    // Workspace membership to open the replay session in the source's
    // Workspace, and without this line the read throws "cannot get property
    // workspaces without inject" (seen on prod v21, replay dead).
    'ui-chat: privasys inject workspaces',
    `  'settingsScope', 'remote', 'remote.session', 'sidebarRight',\n]`,
    `  'settingsScope', 'remote', 'remote.session', 'sidebarRight',\n  'workspaces',\n]`,
  ],
  [
    'ui-chat: privasys imports',
    `import { StatsPills } from './chat/StatsPills.tsx'\n`,
    `import { StatsPills } from './chat/StatsPills.tsx'\n` +
      `import { PrivasysReproducibilityAction, replayTurn, type PrivasysReproInjected } from './chat/PrivasysReproducibility.tsx'\n` +
      `import { PrivasysSamplingChip } from './chat/PrivasysSampling.tsx'\n`,
  ],
  [
    'ui-chat: privasys registrations',
    `  ctx.slots.inject('conversation.approval.detail', () =>\n` +
      `    ctx.slots.register({ name: 'conversation.approval.detail' }, ApprovalCommand))\n`,
    `  ctx.slots.inject('conversation.approval.detail', () =>\n` +
      `    ctx.slots.register({ name: 'conversation.approval.detail' }, ApprovalCommand))\n` +
      `\n` +
      `  // Privasys: the reply's reproducibility block and its faithful replay.\n` +
      `  ctx.slots.inject('conversation.chat.assistant-actions', () =>\n` +
      `    ctx.slots.register({\n` +
      `      name: 'conversation.chat.assistant-actions', id: 'privasys-reproducibility', order: 20, locale: NS,\n` +
      `      inject: (sessionId: SessionId): PrivasysReproInjected => ({\n` +
      `        replay: request => replayTurn(ctx, sessionId, request),\n` +
      `      }),\n` +
      `    }, PrivasysReproducibilityAction))\n` +
      `\n` +
      `  // Privasys: per-session sampling pins, applied by the measured proxy.\n` +
      `  ctx.slots.inject('conversation.input.right', () =>\n` +
      `    ctx.slots.register({\n` +
      `      name: 'conversation.input.right', id: 'privasys-sampling', order: 10, locale: NS,\n` +
      `    }, PrivasysSamplingChip))\n`,
  ],
])
edit('packages/client/ui-chat/src/client/locale.ts', [
  [
    'ui-chat zh: reproducibility keys',
    `  'message.turnUsage.title': '本轮用量',`,
    `  'message.turnUsage.title': '本轮用量',\n` +
      `  'message.repro.pill': '种子 {seed}',\n` +
      `  'message.repro.pillNoSeed': '可复现性',\n` +
      `  'message.repro.title': '可复现性',\n` +
      `  'message.repro.hint': '相同的模型、权重、种子与提示在相同硬件上逐字节复现此回复。',\n` +
      `  'message.repro.request': '请求',\n` +
      `  'message.repro.model': '模型',\n` +
      `  'message.repro.prompt': '提示',\n` +
      `  'message.repro.seed': '种子',\n` +
      `  'message.repro.temperature': '温度',\n` +
      `  'message.repro.topP': 'Top-p',\n` +
      `  'message.repro.topK': 'Top-k',\n` +
      `  'message.repro.maxTokens': '最大输出',\n` +
      `  'message.repro.steps': '模型调用次数',\n` +
      `  'message.repro.quantization': '量化',\n` +
      `  'message.repro.vllm': 'vLLM',\n` +
      `  'message.repro.cuda': 'CUDA',\n` +
      `  'message.repro.gpu': 'GPU',\n` +
      `  'message.repro.tee': 'TEE',\n` +
      `  'message.repro.imageDigest': '镜像摘要',\n` +
      `  'message.repro.kvCache': 'KV 缓存',\n` +
      `  'message.repro.batchInvariance': '批次不变性',\n` +
      `  'message.repro.promptDigest': '提示摘要',\n` +
      `  'message.repro.dynamicContext': '注入时钟',\n` +
      `  'message.repro.replay': '重放本轮',\n` +
      `  'message.repro.replaying': '重放中…',\n` +
      `  'message.repro.replayUnavailable': '重放需要本轮每次调用都记录了可复现性信息',\n` +
      `  'message.repro.replayNotStrict': '此回复使用了共享前缀缓存，无法逐字节重放。先在“采样”中固定种子，该会话的每次调用都会重新预填充，重放即可完全一致。',\n` +
      `  'message.repro.copy': '复制 JSON',\n` +
      `  'message.repro.copied': '已复制',\n` +
      `  'message.repro.replayOf': '第 {turn} 轮的重放',\n` +
      `  'message.repro.replayStep': '第 {step} 步',\n` +
      `  'message.repro.promptMatch': '提示一致',\n` +
      `  'message.repro.promptDiffers': '提示不同',\n` +
      `  'message.repro.clockMatch': '注入时钟一致',\n` +
      `  'message.repro.clockDiffers': '模型注入了不同的时钟',\n` +
      `  'message.repro.replyMatch': '回复一致',\n` +
      `  'message.repro.replyDiffers': '回复不同',\n` +
      `  'message.repro.replySkipped': '此次调用与记录的形状不符',\n` +
      `  'message.repro.toolsReplayed': '已重放 {count} 次工具结果',\n` +
      `  'message.repro.replyBeyond': '此次调用超出了记录的步骤：模型走了更长的路径，种子为新生成',\n` +
      `  'input.sampling': '采样',\n` +
      `  'input.sampling.title': '采样固定',\n` +
      `  'input.sampling.hint': '固定到本会话的每次模型调用。留空则使用默认值。',\n` +
      `  'input.sampling.apply': '应用',\n` +
      `  'input.sampling.clear': '清除',\n` +
      `  'input.sampling.armedShort': '重放已就绪',\n` +
      `  'input.sampling.armed': '重放已就绪：还有 {remaining} 次调用',\n` +
      `  'input.sampling.disarm': '取消重放',\n` +
      `  'input.sampling.seed': '种子',\n` +
      `  'input.sampling.temperature': '温度',\n` +
      `  'input.sampling.topP': 'Top-p',\n` +
      `  'input.sampling.topK': 'Top-k',\n` +
      `  'input.sampling.maxTokens': '最大输出',\n` +
      `  'input.sampling.error': '无法保存：{message}',`,
  ],
  [
    'ui-chat en: reproducibility keys',
    `  'message.turnUsage.title': 'Turn usage',`,
    `  'message.turnUsage.title': 'Turn usage',\n` +
      `  'message.repro.pill': 'Seed {seed}',\n` +
      `  'message.repro.pillNoSeed': 'Reproducibility',\n` +
      `  'message.repro.title': 'Reproducibility',\n` +
      `  'message.repro.hint': 'The same model, weights, seed and prompt on the same hardware reproduce this reply byte for byte.',\n` +
      `  'message.repro.request': 'Request',\n` +
      `  'message.repro.model': 'Model',\n` +
      `  'message.repro.prompt': 'Prompt',\n` +
      `  'message.repro.seed': 'Seed',\n` +
      `  'message.repro.temperature': 'Temperature',\n` +
      `  'message.repro.topP': 'Top-p',\n` +
      `  'message.repro.topK': 'Top-k',\n` +
      `  'message.repro.maxTokens': 'Max tokens',\n` +
      `  'message.repro.steps': 'Model calls',\n` +
      `  'message.repro.quantization': 'Quantization',\n` +
      `  'message.repro.vllm': 'vLLM',\n` +
      `  'message.repro.cuda': 'CUDA',\n` +
      `  'message.repro.gpu': 'GPU',\n` +
      `  'message.repro.tee': 'TEE',\n` +
      `  'message.repro.imageDigest': 'Image digest',\n` +
      `  'message.repro.kvCache': 'KV cache',\n` +
      `  'message.repro.batchInvariance': 'Batch invariance',\n` +
      `  'message.repro.promptDigest': 'Prompt digest',\n` +
      `  'message.repro.dynamicContext': 'Clock injected',\n` +
      `  'message.repro.replay': 'Replay this turn',\n` +
      `  'message.repro.replaying': 'Replaying…',\n` +
      `  'message.repro.replayUnavailable': 'Replay needs a recorded block for every model call of this turn',\n` +
      `  'message.repro.replayNotStrict': 'This reply was served from the shared prefix cache and cannot be replayed byte for byte. Pin a seed in Sampling first: every call of that session then prefills fresh, and its replays match exactly.',\n` +
      `  'message.repro.copy': 'Copy JSON',\n` +
      `  'message.repro.copied': 'Copied',\n` +
      `  'message.repro.replayOf': 'Replay of turn {turn}',\n` +
      `  'message.repro.replayStep': 'step {step}',\n` +
      `  'message.repro.promptMatch': 'prompt identical',\n` +
      `  'message.repro.promptDiffers': 'prompt differs',\n` +
      `  'message.repro.clockMatch': 'injected clock identical',\n` +
      `  'message.repro.clockDiffers': 'the model stamped a different clock',\n` +
      `  'message.repro.replyMatch': 'reply identical',\n` +
      `  'message.repro.replyDiffers': 'reply differs',\n` +
      `  'message.repro.replySkipped': 'this call did not match the recorded shape',\n` +
      `  'message.repro.toolsReplayed': '{count} tool result(s) replayed from the record',\n` +
      `  'message.repro.replyBeyond': 'beyond the recorded steps: the model took a longer path, this call has a fresh seed',\n` +
      `  'input.sampling': 'Sampling',\n` +
      `  'input.sampling.title': 'Sampling pins',\n` +
      `  'input.sampling.hint': 'Pinned for every model call of this session. Leave a field empty to keep the default.',\n` +
      `  'input.sampling.apply': 'Apply',\n` +
      `  'input.sampling.clear': 'Clear',\n` +
      `  'input.sampling.armedShort': 'Replay armed',\n` +
      `  'input.sampling.armed': 'Replay armed: {remaining} call(s) pending',\n` +
      `  'input.sampling.disarm': 'Disarm',\n` +
      `  'input.sampling.seed': 'Seed',\n` +
      `  'input.sampling.temperature': 'Temperature',\n` +
      `  'input.sampling.topP': 'Top-p',\n` +
      `  'input.sampling.topK': 'Top-k',\n` +
      `  'input.sampling.maxTokens': 'Max tokens',\n` +
      `  'input.sampling.error': 'Could not save: {message}',`,
  ],
])

// --- 2h. no DeepSeek in the user's face -------------------------------------
// The adapter behind our `deepseek-official` route talks to Confidential AI
// through the egress proxy, yet its provider heading (the model picker's
// group title), its error lines and a few settings strings still said
// "DeepSeek": a refused or offline attested model surfaced in the chat as
// "DeepSeek API error (HTTP 502)" (2026-09-10). The proxy now shapes every
// model-leg error so dsh prints OUR message (proxy/cmd/egress-proxy/
// modelerror.go); these edits cover what the adapter and the client say on
// their own. Wire identifiers (the provider route, the settings namespace,
// the package names) are untouched: they are addresses, not words. The
// upstream onboarding and welcome dialogs are already unmounted (2g).
edit('packages/llm/llm-deepseek/src/adapter.ts', [
  [
    'provider heading',
    `    return { id: provider, name: 'DeepSeek' }`,
    `    return { id: provider, name: 'Privasys' }`,
  ],
  [
    'image input refusal',
    `          \`DeepSeek model "\${options.model}" does not accept image input.\`,`,
    `          \`Model "\${options.model}" does not accept image input.\`,`,
  ],
  [
    'image conversion',
    `          'DeepSeek image conversion requires the durable attachment service.',`,
    `          'Image conversion requires the durable attachment service.',`,
  ],
  [
    'stream idle timeout',
    `          \`DeepSeek stream idle timeout after \${connection.streamIdleTimeoutMs}ms\`,`,
    `          \`Attested model stream idle timeout after \${connection.streamIdleTimeoutMs}ms\`,`,
  ],
  [
    'aborted by caller',
    `        throw new LlmError('DeepSeek request aborted by caller', 'ABORTED', { cause: error })`,
    `        throw new LlmError('Model request aborted by caller', 'ABORTED', { cause: error })`,
  ],
  [
    'stream failed',
    `      throw new LlmError(\`DeepSeek API stream from \${connection.baseURL} failed\`, 'TRANSPORT', { cause: error })`,
    `      throw new LlmError(\`Attested model stream from \${connection.baseURL} failed\`, 'TRANSPORT', { cause: error })`,
  ],
  [
    'consumer stopped',
    `      consumer.abort('DeepSeek stream consumer stopped')`,
    `      consumer.abort('Model stream consumer stopped')`,
  ],
  [
    'extension preparation',
    `        throw new LlmError('DeepSeek request extension preparation failed', 'REQUEST_EXTENSION', { cause: error })`,
    `        throw new LlmError('Model request extension preparation failed', 'REQUEST_EXTENSION', { cause: error })`,
  ],
  [
    'extension collision',
    `          throw new LlmError(\`DeepSeek request extension field \${JSON.stringify(field)} collides with the base request\`, 'REQUEST_EXTENSION')`,
    `          throw new LlmError(\`Model request extension field \${JSON.stringify(field)} collides with the base request\`, 'REQUEST_EXTENSION')`,
  ],
  [
    'request failed',
    `          \`DeepSeek API request to \${connection.baseURL} failed\`,`,
    `          \`Attested model request to \${connection.baseURL} failed\`,`,
  ],
  [
    'api error line',
    `        let message = \`DeepSeek API error (HTTP \${response.status})\``,
    `        let message = \`Attested model error (HTTP \${response.status})\``,
  ],
  [
    'http cause',
    `          cause: new Error(rawResponse.length > 0 ? rawResponse : \`DeepSeek HTTP \${response.status}\`),`,
    `          cause: new Error(rawResponse.length > 0 ? rawResponse : \`Attested model HTTP \${response.status}\`),`,
  ],
  [
    'extension acceptance',
    `        throw new LlmError('DeepSeek request extension acceptance failed', 'REQUEST_EXTENSION', { cause: error })`,
    `        throw new LlmError('Model request extension acceptance failed', 'REQUEST_EXTENSION', { cause: error })`,
  ],
  [
    'empty response',
    `        throw new LlmError('DeepSeek API returned no response body', 'EMPTY_RESPONSE')`,
    `        throw new LlmError('Attested model returned no response body', 'EMPTY_RESPONSE')`,
  ],
])
edit('packages/llm/llm-deepseek/src/serialize.ts', [
  [
    'effort unsupported',
    `    \`DeepSeek does not support reasoning effort "\${effort}"\`,`,
    `    \`The attested model does not support reasoning effort "\${effort}"\`,`,
  ],
  [
    'effort disabled',
    `      \`DeepSeek deployment does not support reasoning effort "\${effort}"\`,`,
    `      \`This deployment does not support reasoning effort "\${effort}"\`,`,
  ],
  [
    'image content',
    `    throw new LlmError('The DeepSeek chat-completions adapter does not support image content.', 'UNSUPPORTED_CONTENT')`,
    `    throw new LlmError('The attested model adapter does not support image content.', 'UNSUPPORTED_CONTENT')`,
  ],
  [
    'image role',
    `        \`The DeepSeek chat-completions adapter cannot represent image content in a \${message.role} message.\`,`,
    `        \`The attested model adapter cannot represent image content in a \${message.role} message.\`,`,
  ],
  [
    'image not prepared (block)',
    `      \`DeepSeek request image \${block.attachment.attachmentId} was not prepared.\`,`,
    `      \`Request image \${block.attachment.attachmentId} was not prepared.\`,`,
  ],
  [
    'image not prepared (ref)',
    `        throw new LlmError(\`DeepSeek request image \${ref.attachmentId} was not prepared.\`, 'INVALID_REQUEST')`,
    `        throw new LlmError(\`Request image \${ref.attachmentId} was not prepared.\`, 'INVALID_REQUEST')`,
  ],
])
// Settings > Models: the base-URL placeholder named DeepSeek's public API,
// the one endpoint this deployment must never reach. Name our route instead.
edit('packages/client/ui-settings-models/src/client/ProviderEditor.tsx', [
  [
    'base-url placeholder',
    `const DEEPSEEK_PUBLIC_BASE_URL = 'https://api.deepseek.com'`,
    `// Privasys: the attested egress proxy's model route, never a public API.\n` +
      `const DEEPSEEK_PUBLIC_BASE_URL = 'http://127.0.0.1:9411/model/v1'`,
  ],
])
edit('packages/client/ui-settings-models/src/client/locales.ts', [
  [
    'onboarding description (en)',
    `  onboardingDescription: 'Configure the official DeepSeek provider to start building.',`,
    `  onboardingDescription: 'Configure the attested model provider to start building.',`,
  ],
  [
    'onboarding description (zh)',
    `  onboardingDescription: '配置 DeepSeek 官方模型，即可开始使用。',`,
    `  onboardingDescription: '配置已认证的模型提供方，即可开始使用。',`,
  ],
])
edit('packages/client/ui-settings-plugins/src/client/locales.ts', [
  [
    'web search description (en)',
    `  webSearchDescription: 'The DeepSeek search provider.',`,
    `  webSearchDescription: 'The web search provider.',`,
  ],
  [
    'web search description (zh)',
    `  webSearchDescription: 'DeepSeek 搜索提供方。',`,
    `  webSearchDescription: '网页搜索提供方。',`,
  ],
])
// The Files API paths (image attachments) are unreachable behind our text-only
// catalogue, but their messages are thrown from the same adapter: reword them
// so the guard below holds and no path can ever print the old name.
edit('packages/llm/llm-deepseek/src/adapter.ts', [
  [
    'files api image resolve',
    `super('DeepSeek Files API could not resolve a request image.', { cause })`,
    `super('The model files API could not resolve a request image.', { cause })`,
  ],
  [
    'normalized image rejected (facts)',
    `return \`DeepSeek rejected normalized image \${normalizedImageFacts(target)}: \${providerMessage}. \``,
    `return \`The model rejected normalized image \${normalizedImageFacts(target)}: \${providerMessage}. \``,
  ],
  [
    'normalized image rejected (candidates)',
    `return \`DeepSeek rejected a normalized request image: \${providerMessage}. Candidate images: \``,
    `return \`The model rejected a normalized request image: \${providerMessage}. Candidate images: \``,
  ],
])
// Guard for the next re-pin: no prose "DeepSeek" may remain in a message the
// adapter can throw (a string literal starting with the word). Identifiers
// and comments are fine; a new upstream message is the signal to extend 2h.
for (const rel of ['packages/llm/llm-deepseek/src/adapter.ts', 'packages/llm/llm-deepseek/src/serialize.ts']) {
  const lines = readFileSync(join(dsh, rel), 'utf8').split('\n')
  const prose = lines
    .map((line, i) => [i + 1, line])
    .filter(([, line]) => /['`"](The )?DeepSeek /.test(line) && !/^\s*(\/\/|\*|\/\*)/.test(line))
  if (prose.length > 0) {
    throw new Error(`overlay 2h: user-facing DeepSeek wording remains in ${rel}: ` +
      prose.map(([n, l]) => `${n}: ${l.trim()}`).join(' | '))
  }
}

// --- 2i. Delete session -------------------------------------------------------
// dsh can archive a session (a registry flag; the log stays) but never delete
// one: no menu item, no Host command, no persistence operation. On the
// platform a session's durable home is the holder's Drive, and the mirror
// now removes what no longer exists locally, so deletion is one Host command
// away. It runs in the session controller, which owns the live Agents and
// already emits the removal event the sidebar listens to:
//   1. a live but idle Agent is released (its handle, which the controller
//      dropped upstream, is now retained) — a running one is refused;
//   2. the session directory (log, lock, attachments) is removed through the
//      JSONL persistence, which learns a `deleteSession`;
//   3. the workspace registry forgets the id (membership, archive set, index);
//   4. `api-session/removed` reaches the client for a cold session (a live
//      one emits it through its own disposal).
// The client gains `delete` on the session service, and the workspace browser
// a "Delete session" row action behind a confirmation dialog. The row reaches
// the dialog over a module-level bus (PrivasysSessionDelete.ts) rather than
// through the four-hop prop chain, so upstream layout changes cost nothing.

/** Like edit(), but the anchor must occur exactly `count` times and every occurrence is replaced. */
function editAll(rel, transforms) {
  const path = join(dsh, rel)
  let src = readFileSync(path, 'utf8')
  for (const [label, find, replace, count] of transforms) {
    const found = src.split(find).length - 1
    if (found !== count) {
      throw new Error(`overlay anchor MISSING in ${rel}: "${label}" (expected ${count} occurrence(s), found ${found}). Upstream changed — rebase the patch.`)
    }
    src = src.split(find).join(replace)
  }
  writeFileSync(path, src)
  console.log(`[overlay] patched ${rel}`)
}

// (a) session controller: retain Agent handles so one Agent can be released.
editAll('packages/api/session-controller/src/agent.ts', [
  [
    'agent handle map',
    `  private readonly creations = new Map<SessionId, Promise<Agent>>()`,
    `  private readonly creations = new Map<SessionId, Promise<Agent>>()\n` +
      `  // Privasys: the handles create/resume hand back, so a session can be\n` +
      `  // released on its own (dsh keeps every resumed Agent until unload).\n` +
      `  private readonly handles = new Map<SessionId, { agent: Agent; dispose(): Promise<void> }>()`,
    1,
  ],
  [
    'resume handle retained (4)',
    `    return (await this.ctx.agents.resume({
` +
      `      resumeSessionId: sessionId,
` +
      `      agentOptions: this.agentOptions(),
` +
      `      setup: composition.setup,
` +
      `    })).agent`,
    `    return this.retain(await this.ctx.agents.resume({
` +
      `      resumeSessionId: sessionId,
` +
      `      agentOptions: this.agentOptions(),
` +
      `      setup: composition.setup,
` +
      `    }))`,
    1,
  ],
  [
    'resume handle retained (8)',
    `        return (await this.ctx.agents.resume({
` +
      `          resumeSessionId: sessionId,
` +
      `          agentOptions: this.agentOptions(),
` +
      `          setup: composition.setup,
` +
      `        })).agent`,
    `        return this.retain(await this.ctx.agents.resume({
` +
      `          resumeSessionId: sessionId,
` +
      `          agentOptions: this.agentOptions(),
` +
      `          setup: composition.setup,
` +
      `        }))`,
    1,
  ],
  [
    'create handle retained',
    `    return (await this.ctx.agents.create({\n` +
      `      sessionId,\n` +
      `      agentOptions: this.agentOptions(),\n` +
      `      meta: {\n` +
      `        cwd,\n` +
      `        ...(composition.agentPreset === undefined ? {} : { agentPreset: composition.agentPreset }),\n` +
      `      },\n` +
      `      setup: composition.setup,\n` +
      `    })).agent\n` +
      `  }`,
    `    return this.retain(await this.ctx.agents.create({\n` +
      `      sessionId,\n` +
      `      agentOptions: this.agentOptions(),\n` +
      `      meta: {\n` +
      `        cwd,\n` +
      `        ...(composition.agentPreset === undefined ? {} : { agentPreset: composition.agentPreset }),\n` +
      `      },\n` +
      `      setup: composition.setup,\n` +
      `    }))\n` +
      `  }\n` +
      `\n` +
      `  /** Privasys: remember one live Agent's handle and hand back its Agent. */\n` +
      `  private retain(handle: { agent: Agent; dispose(): Promise<void> }): Agent {\n` +
      `    this.handles.set(handle.agent.id, handle)\n` +
      `    return handle.agent\n` +
      `  }\n` +
      `\n` +
      `  /**\n` +
      `   * Privasys: release one live Agent — stop and drain it, unregister it,\n` +
      `   * detach its session (which emits \`session/disposed\`, hence\n` +
      `   * \`api-session/removed\`). A session this controller never resumed is a\n` +
      `   * no-op.\n` +
      `   */\n` +
      `  async release(sessionId: SessionId): Promise<boolean> {\n` +
      `    const handle = this.handles.get(sessionId)\n` +
      `    if (handle === undefined) return false\n` +
      `    this.handles.delete(sessionId)\n` +
      `    await handle.dispose()\n` +
      `    return true\n` +
      `  }`,
    1,
  ],
])

// (b) session controller: the delete command, its types and its Remote.
edit('packages/api/session-controller/src/types.ts', [
  [
    'delete request/value types',
    `/** Receipt after cancellation is admitted to the live Agent. */\n` +
      `export interface SessionCancelValue {\n` +
      `  readonly accepted: true\n` +
      `}`,
    `/** Receipt after cancellation is admitted to the live Agent. */\n` +
      `export interface SessionCancelValue {\n` +
      `  readonly accepted: true\n` +
      `}\n` +
      `\n` +
      `/** Privasys: request to delete one Session for good (log, registry accounting, Drive copy). */\n` +
      `export interface SessionDeleteRequest {\n` +
      `  readonly sessionId: SessionId\n` +
      `}\n` +
      `\n` +
      `/** Privasys: receipt naming the deleted Session. */\n` +
      `export interface SessionDeleteValue {\n` +
      `  readonly sessionId: SessionId\n` +
      `}`,
  ],
])
edit('packages/api/session-controller/src/commands.ts', [
  [
    'delete type imports',
    `  SessionCancelRequest,\n  SessionCancelValue,\n  SessionCreateRequest,`,
    `  SessionCancelRequest,\n  SessionCancelValue,\n  SessionDeleteRequest,\n  SessionDeleteValue,\n  SessionCreateRequest,`,
  ],
  [
    'delete command',
    `    agent.cancel({ kind: 'user' }, { keepInbox: true })\n` +
      `    return { accepted: true }\n` +
      `  }`,
    `    agent.cancel({ kind: 'user' }, { keepInbox: true })\n` +
      `    return { accepted: true }\n` +
      `  }\n` +
      `\n` +
      `  /**\n` +
      `   * Privasys: delete one ordinary Session for good — its live Agent (idle\n` +
      `   * only), its stored log, and its registry accounting. The platform\n` +
      `   * mirror removes the Drive copy on its next pass.\n` +
      `   * @param request - Session to delete.\n` +
      `   * @returns the deleted Session identity.\n` +
      `   */\n` +
      `  async delete(request: SessionDeleteRequest): Promise<SessionDeleteValue> {\n` +
      `    const { sessionId } = request\n` +
      `    const live = this.ctx.agents.get(sessionId)\n` +
      `    if (live !== undefined) {\n` +
      `      if (hasApiSessionSubagentOwner(this.ctx, live.session, live)) {\n` +
      `        throw apiSessionSubagentOwnershipError(sessionId)\n` +
      `      }\n` +
      `      if (live.status === 'running') {\n` +
      `        throw new RemoteError(\n` +
      `          'gateway/bad-request',\n` +
      `          \`session "\${sessionId}" is still running; stop it before deleting it\`,\n` +
      `          {},\n` +
      `        )\n` +
      `      }\n` +
      `      // Released first: the disposal closes the log and drops the lease,\n` +
      `      // and its own \`session/disposed\` carries the removal to the client.\n` +
      `      await this.agents.release(sessionId)\n` +
      `    }\n` +
      `    const persistence = this.ctx.sessionPersistence as { deleteSession?(id: SessionId): Promise<boolean> }\n` +
      `    let removed = false\n` +
      `    if (persistence.deleteSession !== undefined) {\n` +
      `      try {\n` +
      `        removed = await persistence.deleteSession(sessionId)\n` +
      `      } catch (error: unknown) {\n` +
      `        throw new RemoteError('gateway/internal', \`failed to delete session "\${sessionId}": \${String(error)}\`, {})\n` +
      `      }\n` +
      `    }\n` +
      `    if (!removed && live === undefined) {\n` +
      `      throw new RemoteError('session/not-found', \`session "\${sessionId}" not found\`, { sessionId })\n` +
      `    }\n` +
      `    const registry = this.ctx.workspaceRegistry as { forgetSession?(id: SessionId): Promise<void> }\n` +
      `    await registry.forgetSession?.(sessionId)\n` +
      `    if (live === undefined) this.ctx.emit('api-session/removed', sessionId)\n` +
      `    return { sessionId }\n` +
      `  }`,
  ],
])
edit('packages/api/session-controller/src/index.ts', [
  [
    'delete type imports (host)',
    `  SessionCancelRequest,\n  SessionCancelValue,`,
    `  SessionCancelRequest,\n  SessionCancelValue,\n  SessionDeleteRequest,\n  SessionDeleteValue,`,
  ],
  [
    'delete remote',
    `  @Remote('cancel')\n` +
      `  cancel(request: SessionCancelRequest): SessionCancelValue {\n` +
      `    return this.commands.cancel(request)\n` +
      `  }`,
    `  @Remote('cancel')\n` +
      `  cancel(request: SessionCancelRequest): SessionCancelValue {\n` +
      `    return this.commands.cancel(request)\n` +
      `  }\n` +
      `\n` +
      `  /**\n` +
      `   * Privasys: delete one Session for good (see SessionCommandController.delete).\n` +
      `   * @param request - Session to delete.\n` +
      `   * @returns the deleted Session identity.\n` +
      `   */\n` +
      `  @Remote('delete')\n` +
      `  delete(request: SessionDeleteRequest): Promise<SessionDeleteValue> {\n` +
      `    return this.commands.delete(request)\n` +
      `  }`,
  ],
])

// (c) JSONL persistence: remove one stored session's directory.
edit('packages/session/session-persistence-jsonl/src/index.ts', [
  [
    'persistence deleteSession',
    `  /**\n` +
      `   * List every stored session visible to this process: materialized artifacts\n` +
      `   * plus this process's created-but-unmaterialized sessions.`,
    `  /**\n` +
      `   * Privasys: remove one stored session's directory — its log, lock and\n` +
      `   * anything filed beside them. Every live handle must have been released\n` +
      `   * first; a session still being created is refused.\n` +
      `   * @param id - the session to remove.\n` +
      `   * @returns true when a stored session was removed, false when none existed.\n` +
      `   */\n` +
      `  async deleteSession(id: SessionId): Promise<boolean> {\n` +
      `    await this.ensureRootEncoding()\n` +
      `    if (this.tracker.pendingOf(id) !== undefined) {\n` +
      `      throw new Error(\`session "\${id}" is still being created\`)\n` +
      `    }\n` +
      `    const selected = await this.findLog(id)\n` +
      `    if (selected === undefined) return false\n` +
      `    this.coldLogMemo.delete(id)\n` +
      `    this.migrationPreparations.delete(id)\n` +
      `    await rm(dirname(selected.currentPath), { recursive: true, force: true })\n` +
      `    return true\n` +
      `  }\n` +
      `\n` +
      `  /**\n` +
      `   * List every stored session visible to this process: materialized artifacts\n` +
      `   * plus this process's created-but-unmaterialized sessions.`,
  ],
])

// (d) workspace registry: forget a deleted session everywhere it is counted.
edit('packages/workspace/workspace/src/index.ts', [
  [
    'registry forgetSession',
    `      const state = this.requireState()\n` +
      `      await this.setState({ ...state, archivedSessionIds: [...state.archivedSessionIds, sessionId] })\n` +
      `    })\n` +
      `  }`,
    `      const state = this.requireState()\n` +
      `      await this.setState({ ...state, archivedSessionIds: [...state.archivedSessionIds, sessionId] })\n` +
      `    })\n` +
      `  }\n` +
      `\n` +
      `  /**\n` +
      `   * Privasys: drop one deleted session from every workspace's membership,\n` +
      `   * from the archive set, and from the header index. Unknown ids are a\n` +
      `   * no-op: the caller has already removed the log.\n` +
      `   * @param sessionId - The deleted session.\n` +
      `   * @returns resolution after durability.\n` +
      `   */\n` +
      `  forgetSession(sessionId: SessionId): Promise<void> {\n` +
      `    return this.enqueueOperation(async () => {\n` +
      `      this.headers.delete(sessionId)\n` +
      `      this.sessionPaths.delete(sessionId)\n` +
      `      this.invalidSessionPaths.delete(sessionId)\n` +
      `      const table = this.requireTable()\n` +
      `      for (const [id, record] of [...table.entries()]) {\n` +
      `        if (!record.sessionIds.includes(sessionId)) continue\n` +
      `        await table.update(id, current => ({\n` +
      `          ...current,\n` +
      `          sessionIds: current.sessionIds.filter(candidate => candidate !== sessionId),\n` +
      `          updatedAt: new Date().toISOString(),\n` +
      `        }))\n` +
      `      }\n` +
      `      const state = this.requireState()\n` +
      `      if (!state.archivedSessionIds.includes(sessionId)) return\n` +
      `      await this.setState({\n` +
      `        ...state,\n` +
      `        archivedSessionIds: state.archivedSessionIds.filter(candidate => candidate !== sessionId),\n` +
      `      })\n` +
      `    })\n` +
      `  }`,
  ],
])

// (e) client session service: \`delete\` beside \`fork\`.
edit('packages/api/session-controller/src/client/sessions/manager.ts', [
  [
    'manager delete',
    `  async fork(\n` +
      `    opts: { sessionId: SessionId; atSeq?: SessionSeq },\n` +
      `  ): Promise<RemoteResult<{ sessionId: SessionId }>> {`,
    `  /** Privasys: delete one Session for good; the Host's removal event drops the row. */\n` +
      `  async delete(sessionId: SessionId): Promise<RemoteResult<{ sessionId: SessionId }>> {\n` +
      `    return this.remote.session.delete({ sessionId })\n` +
      `  }\n` +
      `\n` +
      `  async fork(\n` +
      `    opts: { sessionId: SessionId; atSeq?: SessionSeq },\n` +
      `  ): Promise<RemoteResult<{ sessionId: SessionId }>> {`,
  ],
])
edit('packages/api/session-controller/src/client/sessions/service.ts', [
  [
    'service delete',
    `  async fork(opts: {\n` +
      `    sessionId: SessionId\n` +
      `    atSeq?: number\n` +
      `    increaseTitle?: boolean\n` +
      `  }): Promise<SessionId> {`,
    `  async delete(sessionId: SessionId): Promise<void> {\n` +
      `    const result = await this.manager.delete(sessionId)\n` +
      `    if (!result.ok) throw new Error(\`\${result.error.code}: \${result.error.message}\`)\n` +
      `    this.projectList()\n` +
      `  }\n` +
      `\n` +
      `  async fork(opts: {\n` +
      `    sessionId: SessionId\n` +
      `    atSeq?: number\n` +
      `    increaseTitle?: boolean\n` +
      `  }): Promise<SessionId> {`,
  ],
])
edit('packages/api/session-controller/src/client/contract/sessions.ts', [
  [
    'contract delete',
    `  fork(opts: { sessionId: SessionId; atSeq?: number; increaseTitle?: boolean }): Promise<SessionId>`,
    `  fork(opts: { sessionId: SessionId; atSeq?: number; increaseTitle?: boolean }): Promise<SessionId>\n` +
      `  /**\n` +
      `   * Privasys: delete one session for good (log, registry accounting, Drive\n` +
      `   * copy). A running session is refused.\n` +
      `   * @param sessionId - the session to delete.\n` +
      `   */\n` +
      `  delete?(sessionId: SessionId): Promise<void>`,
  ],
])

// (f) workspace browser: the row action, the dialog, the wiring.
put('packages/client/ui-workspace/src/client/rows/PrivasysSessionDelete.ts', 'overlay/workspace/PrivasysSessionDelete.ts')
// The client-runtime fake of the session Remote namespace must implement
// every generated method, tests included (the image build type-checks them).
edit('packages/api/session-controller/tests/fake-api.client.ts', [
  [
    'fake delete',
    `        cancel: payload => this.record('session.cancel', payload, this.onCancel(payload)),`,
    `        cancel: payload => this.record('session.cancel', payload, this.onCancel(payload)),\n` +
      `        delete: payload => this.record('session.delete', payload, Promise.resolve<RemoteResult<{ sessionId: SessionId }>>({ ok: true, value: { sessionId: payload.sessionId } })),`,
  ],
])

edit('packages/client/ui-workspace/src/client/contract/slots.ts', [
  [
    'slot deleteSession',
    `  archiveSession: (sessionId: SessionId) => Promise<void>`,
    `  archiveSession: (sessionId: SessionId) => Promise<void>\n` +
      `  /**\n` +
      `   * Privasys: delete a Session for good. Deleting the current session\n` +
      `   * clears the selection into the New Session view state.\n` +
      `   */\n` +
      `  deleteSession?: (sessionId: SessionId) => Promise<void>`,
  ],
])
edit('packages/client/ui-workspace/src/client/index.ts', [
  [
    'wire deleteSession',
    `    archiveSession: async (sessionId) => { await uiWorkspace.archiveSession(sessionId) },`,
    `    archiveSession: async (sessionId) => { await uiWorkspace.archiveSession(sessionId) },\n` +
      `    deleteSession: async (sessionId) => { await uiWorkspace.deleteSession(sessionId) },`,
  ],
])
edit('packages/client/ui-workspace/src/client/navigation.ts', [
  [
    'navigation deleteSession (interface)',
    `  archiveSession(sessionId: SessionId): Promise<void>\n` +
      `  /**\n` +
      `   * Open the Host-native directory picker.`,
    `  archiveSession(sessionId: SessionId): Promise<void>\n` +
      `  /**\n` +
      `   * Privasys: delete a Session for good and clear it when it is the current selection.\n` +
      `   * @param sessionId - Session to delete.\n` +
      `   */\n` +
      `  deleteSession(sessionId: SessionId): Promise<void>\n` +
      `  /**\n` +
      `   * Open the Host-native directory picker.`,
  ],
  [
    'navigation deleteSession (impl)',
    `  async archiveSession(sessionId: SessionId): Promise<void> {\n` +
      `    await this.workspaces.archiveSession(sessionId)\n` +
      `  }`,
    `  async archiveSession(sessionId: SessionId): Promise<void> {\n` +
      `    await this.workspaces.archiveSession(sessionId)\n` +
      `  }\n` +
      `\n` +
      `  async deleteSession(sessionId: SessionId): Promise<void> {\n` +
      `    const current = this.sessions.list.getSnapshot().current\n` +
      `    if (this.sessions.delete === undefined) throw new Error('session deletion is unavailable in this build')\n` +
      `    await this.sessions.delete(sessionId)\n` +
      `    if (current === sessionId) this.sessions.clear()\n` +
      `  }`,
  ],
])
edit('packages/client/ui-workspace/src/client/rows/Rows.tsx', [
  [
    'rows: bus import',
    `import css from './Rows.module.css'`,
    `import css from './Rows.module.css'\n` +
      `import { requestSessionDelete } from './PrivasysSessionDelete.ts'`,
  ],
  [
    'rows: delete menu item',
    `    { id: 'archive', label: t('menu.archiveSession'), icon: <IconArchiveOutline20 size={16} /> },\n` +
      `  ]`,
    `    { id: 'archive', label: t('menu.archiveSession'), icon: <IconArchiveOutline20 size={16} /> },\n` +
      `    // Privasys: deletion is destructive, so it confirms in the browser root.\n` +
      `    { id: 'delete', label: t('menu.deleteSession'), icon: <IconTrashOutline16 /> },\n` +
      `  ]`,
  ],
  [
    'rows: delete dispatch',
    `              if (id === 'archive') onArchive(node.id)`,
    `              if (id === 'archive') onArchive(node.id)\n` +
      `              if (id === 'delete') requestSessionDelete(node.id, row.title)`,
  ],
])
edit('packages/client/ui-workspace/src/client/rows/WorkspaceBrowser.tsx', [
  [
    'browser: bus import',
    `import css from './WorkspaceBrowser.module.css'`,
    `import css from './WorkspaceBrowser.module.css'\n` +
      `import { onSessionDeleteRequest } from './PrivasysSessionDelete.ts'`,
  ],
  [
    'browser: deleteSession prop',
    `  archiveSession,\n  insertSessionBefore,\n  createWorkspace,`,
    `  archiveSession,\n  deleteSession,\n  insertSessionBefore,\n  createWorkspace,`,
  ],
  [
    'browser: session delete state',
    `  const [deleteError, setDeleteError] = useState<string | null>(null)`,
    `  const [deleteError, setDeleteError] = useState<string | null>(null)\n` +
      `\n` +
      `  // Privasys: session deletion — the row publishes a request, the root\n` +
      `  // confirms it, the Host deletes, and the removal event drops the row.\n` +
      `  const [sessionDeleteTarget, setSessionDeleteTarget] = useState<{ sessionId: SessionNode['id']; title: string } | null>(null)\n` +
      `  const [sessionDeleting, setSessionDeleting] = useState(false)\n` +
      `  const [sessionDeleteError, setSessionDeleteError] = useState<string | null>(null)\n` +
      `  useEffect(() => onSessionDeleteRequest((request) => {\n` +
      `    setSessionDeleteTarget({ sessionId: request.sessionId as SessionNode['id'], title: request.title })\n` +
      `    setSessionDeleteError(null)\n` +
      `  }), [])\n` +
      `  const closeSessionDelete = () => {\n` +
      `    if (sessionDeleting) return\n` +
      `    setSessionDeleteTarget(null)\n` +
      `    setSessionDeleteError(null)\n` +
      `  }\n` +
      `  const confirmSessionDelete = () => {\n` +
      `    if (sessionDeleting || sessionDeleteTarget === null || deleteSession === undefined) return\n` +
      `    setSessionDeleting(true)\n` +
      `    setSessionDeleteError(null)\n` +
      `    deleteSession(sessionDeleteTarget.sessionId).then(() => {\n` +
      `      setSessionDeleting(false)\n` +
      `      setSessionDeleteTarget(null)\n` +
      `    }).catch((reason: unknown) => {\n` +
      `      setSessionDeleting(false)\n` +
      `      setSessionDeleteError(reason instanceof Error ? reason.message : String(reason))\n` +
      `    })\n` +
      `  }`,
  ],
  [
    'browser: session delete dialog',
    `      <Modal\n` +
      `        open={deleteTarget !== null}\n` +
      `        onClose={closeDelete}`,
    `      <Modal\n` +
      `        open={sessionDeleteTarget !== null}\n` +
      `        onClose={closeSessionDelete}\n` +
      `        closeLabel={t('close')}\n` +
      `        title={t('delete.session')}\n` +
      `        {...sessionDeleteTarget === null\n` +
      `          ? {}\n` +
      `          : { description: t('delete.session.desc', { name: sessionDeleteTarget.title }) }}\n` +
      `        footer={(\n` +
      `          <>\n` +
      `            <Button variant="outline" disabled={sessionDeleting} onClick={closeSessionDelete}>{t('cancel')}</Button>\n` +
      `            <Button\n` +
      `              variant="outline"\n` +
      `              className={css.deleteAction}\n` +
      `              disabled={sessionDeleting}\n` +
      `              onClick={confirmSessionDelete}\n` +
      `            >\n` +
      `              {t('delete.session')}\n` +
      `            </Button>\n` +
      `          </>\n` +
      `        )}\n` +
      `      >\n` +
      `        {sessionDeleting && <div className={css.deleteStatus} role="status">{t('delete.session.pending')}</div>}\n` +
      `        {sessionDeleteError !== null && <div className={css.renameError} role="alert">{sessionDeleteError}</div>}\n` +
      `      </Modal>\n` +
      `      <Modal\n` +
      `        open={deleteTarget !== null}\n` +
      `        onClose={closeDelete}`,
  ],
])
edit('packages/client/ui-workspace/src/client/locales.ts', [
  [
    'locale: delete session (zh)',
    `  'menu.archiveSession': '归档会话',`,
    `  'menu.archiveSession': '归档会话',\n` +
      `  'menu.deleteSession': '删除会话',\n` +
      `  'delete.session': '删除会话',\n` +
      `  'delete.session.desc': '将永久删除“{name}”：本地记录和您 Drive 中的副本都会被移除，无法恢复。',\n` +
      `  'delete.session.pending': '正在删除会话…',`,
  ],
  [
    'locale: delete session (en)',
    `  'menu.archiveSession': 'Archive session',`,
    `  'menu.archiveSession': 'Archive session',\n` +
      `  'menu.deleteSession': 'Delete session',\n` +
      `  'delete.session': 'Delete session',\n` +
      `  'delete.session.desc': 'This deletes “{name}” for good: its record here and the copy in your Drive are removed and cannot be recovered.',\n` +
      `  'delete.session.pending': 'Deleting session…',`,
  ],
])

console.log('[overlay] done')
