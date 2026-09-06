// Apply the Privasys attested-harness web overlay onto a vendored dsh tree.
//
// This is the sanctioned patch-queue divergence (D8: extend-don't-fork), rebased
// for dsh v0.1.2-alpha (the @Remote gateway; the legacy APIProxy/AbstractApiClient
// was removed). It:
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
  mcpFleetRow('drive', 'drive')
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
// preset sessions — rewrite the preset texts themselves.
const PRIVASYS_PERSONA =
  `      You are a coding agent of the Privasys Harness, powered by the {{model}} model running in a hardware-attested confidential enclave. Your working directory is {{cwd}}.`
for (const preset of ['standard', 'ptc']) {
  edit(`packages/preset/agent-presets/presets/${preset}/agent.cordis.yml`, [
    [
      `preset ${preset} persona rebrand`,
      `      You are a coding agent powered by the {{model}} model. Your working directory is {{cwd}}.`,
      PRIVASYS_PERSONA,
    ],
  ])
}
edit('packages/preset/agent-presets/presets/cordis/agent.cordis.yml', [
  [
    'preset cordis persona rebrand',
    `      You are a coding agent powered by the {{model}} model, running on the DeepSeek Harness. Your working directory is {{cwd}}.`,
    PRIVASYS_PERSONA,
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

console.log('[overlay] done')
