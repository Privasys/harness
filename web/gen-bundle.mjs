// Generate @privasys/harness-bundle's cordis.patch.yml from dsh-base's,
// dropping the rows the Privasys deployment does not admit. One-shot tool:
// the OUTPUT is the reviewed artifact committed to the harness repo.
import { readFileSync, writeFileSync } from 'node:fs'

const [src, dst] = process.argv.slice(2)
const DROP = new Set([
  // Built-in public web surface: the agent's only egress is the attested fleet.
  'web', 'web-search-deepseek', 'web-fetch-http', 'tool-web',
  // Generic external-provider adapter: the attested deployment serves exactly
  // one provider (Confidential AI); external models are a future enterprise
  // composition decision, not a default.
  'llm-pi-ai',
  // DeepSeek-cloud reporting surfaces: session-log upload, plugin-inventory
  // metadata in requests, OTEL telemetry.
  'session-log-deepseek', 'plugin-package-inventory-deepseek', 'session-telemetry-otel',
  // Mid-transcript in-place tool-result rewriting: a prefix-cache killer for
  // the confidential inference backend.
  'tool-result-pruner',
  // Windows-only shell surface: dead weight in a Linux enclave.
  'tool-pwsh', 'pwsh-sandbox',
])

const lines = readFileSync(src, 'utf8').split('\n')
const insertAt = lines.findIndex(l => l === '- insert:')
if (insertAt < 0) throw new Error('no insert block')

// Split rows: a block = optional comment/blank run + "    - id: X" + body.
const blocks = []
let pending = [] // comments/blanks awaiting their row
let current = null
for (const line of lines.slice(insertAt + 1)) {
  const m = /^    - id: (\S+)/.exec(line)
  if (m) {
    if (current) blocks.push(current)
    current = { id: m[1], lines: [...pending, line] }
    pending = []
  } else if (current && (/^\s*$/.test(line) || /^    #/.test(line))) {
    // could belong to the NEXT row — buffer, attach on decision
    pending.push(line)
  } else if (current) {
    current.lines.push(...pending, line)
    pending = []
  } else {
    pending.push(line)
  }
}
if (current) blocks.push(current)

const seen = blocks.map(b => b.id)
const missing = [...DROP].filter(id => !seen.includes(id))
if (missing.length) throw new Error(`DROP ids not found in base: ${missing.join(', ')} — rebase the list.`)

const kept = blocks.filter(b => !DROP.has(b.id))

// Privasys defaults, pinned at the bundle level so no profile composed over
// it can start on DeepSeek's cloud model or public endpoint. The same values
// are restated in app/profile.cordis.yml (a patch replaces a row's whole
// config); keep the two in step.
const MODEL_ROW = [
  '      config:',
  '        baseURL: http://127.0.0.1:9411/model/v1',
  '        thinking: enabled',
  '        reasoningEffort: low',
  '        models:',
  '          - id: qwen36-35b-a3b-fp8',
  '            name: Qwen3.6 35B (Privasys attested)',
  '            contextWindow: 131072',
  '            maxTokens: 8192',
]
for (const b of kept) {
  if (b.id === 'agent-default-model') {
    const i = b.lines.findIndex(l => /^\s+model: /.test(l))
    if (i < 0) throw new Error('agent-default-model row has no model line')
    b.lines[i] = b.lines[i].replace(/model: .*/, 'model: qwen36-35b-a3b-fp8')
    const row = b.lines.findIndex(l => /^    - id: /.test(l))
    b.lines.splice(row, 0,
      '    # Privasys: the default is OUR attested model, not DeepSeek\x27s cloud one, so',
      '    # every profile composed over this bundle (deployment, smoke, headless)',
      '    # starts on it even before app/profile.cordis.yml restates it.')
  }
  if (b.id === 'llm-deepseek') {
    if (b.lines.some(l => /^\s+config:/.test(l))) throw new Error('llm-deepseek row now carries config upstream; merge by hand')
    const name = b.lines.findIndex(l => /^      name: /.test(l))
    b.lines.splice(name + 1, 0,
      '      # Privasys: the route is pinned to the attested egress proxy and the',
      '      # catalogue is our model alone, at the bundle level too, so no profile',
      '      # can fall back to the adapter\x27s DeepSeek catalogue or public endpoint.',
      '      # app/profile.cordis.yml restates the same values (a patch replaces the',
      '      # whole config); keep the two in step.',
      ...MODEL_ROW)
  }
}
const header = `# @privasys/harness-bundle — the Privasys Privasys Harness core composition.
#
# ALLOW-LIST fork of @deepseek-ai/dsh-base (generated from the pinned dsh tree,
# then reviewed + committed — regenerate with web/gen-bundle.mjs on a re-pin
# and REVIEW THE DIFF). Every row here is explicitly admitted; a new upstream
# default plugin does NOT flow into this deployment until it is added here.
#
# Rows deliberately absent (vs dsh-base):
#   web, web-search-deepseek, web-fetch-http, tool-web  — the agent's only
#     egress is the attested tool fleet via the egress proxy (fail closed)
#   llm-pi-ai                — no external model providers by default
#   session-log-deepseek     — no session-log upload to DeepSeek
#   plugin-package-inventory-deepseek — no plugin metadata in model requests
#   session-telemetry-otel   — no telemetry
#   tool-result-pruner       — mid-transcript in-place rewrites bust the
#     confidential backend's prefix cache
#   tool-pwsh, pwsh-sandbox  — Windows-only, dead weight in a Linux enclave
#
# Deployment config (persona, time-context, model route, attested MCP tools)
# stays in app/profile.cordis.yml — the launcher --patch layer over this.

- insert:
`
writeFileSync(dst, header + kept.map(b => b.lines.join('\n').replace(/\s+$/,'')).join('\n\n') + '\n')
console.log(`kept ${kept.length}/${blocks.length} rows; dropped: ${blocks.filter(b=>DROP.has(b.id)).map(b=>b.id).join(', ')}`)
