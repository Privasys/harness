/**
 * The holder's agents, as the proxy lists them (agents_api.go): one store for
 * the page, read by the agent tab (PrivasysAgentTab.tsx). Read on demand, on
 * the tab opening or the person asking; the folder itself changes through
 * the chat, and a run's effects show as sessions, so nothing here is pushed.
 *
 * Every call rides the sealed transport the shell installs: the proxy answers
 * for the relay-asserted subject only.
 */

export interface AgentTrigger {
  every?: string
  at?: string
  on?: string
}

export interface AgentItem {
  name: string
  /** The folder its sessions run in; a session's cwd is inside it. */
  path: string
  paused: boolean
  /** True when a trigger is set and the agent is not paused. */
  scheduled: boolean
  prompt?: string
  trigger?: AgentTrigger
  debounce: string
  min_interval: string
  /** agent.md */
  instructions: string
  /** agent.yaml */
  definition: string
}

export interface AgentsView {
  agents: AgentItem[]
  loaded: boolean
  /** The last read's failure, "" when the last read succeeded. */
  problem: string
}

function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

let view: AgentsView = { agents: [], loaded: false, problem: '' }
const listeners = new Set<() => void>()
let started = false

function set(patch: Partial<AgentsView>): void {
  view = { ...view, ...patch }
  for (const l of listeners) l()
}

export async function refresh(): Promise<void> {
  try {
    const r = await pvFetch('/privasys/agents')
    const d = await r.json() as { agents?: AgentItem[]; error?: string }
    if (!r.ok) { set({ loaded: true, problem: d.error ?? 'The agents could not be read.' }); return }
    set({ agents: Array.isArray(d.agents) ? d.agents : [], loaded: true, problem: '' })
  } catch {
    set({ loaded: true, problem: 'The agents could not be read; try again in a moment.' })
  }
}

export function subscribe(listener: () => void): () => void {
  if (!started) { started = true; void refresh() }
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function snapshot(): AgentsView {
  return view
}

/** The agent whose folder holds this working directory, if any. */
export function agentAt(agents: readonly AgentItem[], cwd: string | undefined): AgentItem | undefined {
  if (cwd === undefined || cwd === '') return undefined
  const dir = cwd.replace(/\\/g, '/').replace(/\/+$/, '')
  return agents.find(a => {
    const root = a.path.replace(/\\/g, '/').replace(/\/+$/, '')
    return root !== '' && (dir === root || dir.startsWith(root + '/'))
  })
}

/** Pause or resume one agent; resolves to an error sentence, or "" on success. */
export async function setPaused(name: string, paused: boolean): Promise<string> {
  try {
    const r = await pvFetch(`/privasys/agents/${encodeURIComponent(name)}/paused`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ paused }),
    })
    const d = await r.json() as { error?: string }
    if (!r.ok) return d.error ?? 'The change could not be made.'
    await refresh()
    return ''
  } catch {
    return 'The change could not be made; try again in a moment.'
  }
}
