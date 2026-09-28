// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.
//
// Privasys connectors: which of the attested connectors a conversation may use.
//
// Every connector (mail, calendar, files, meetings, drive, web search...) is
// one MCP server behind the egress proxy, mounted in every agent preset, so
// every session sees every connector's tools. That is the right default for
// the tools, and the wrong one for a person who wants this conversation to
// stay out of their mailbox. This plugin is the switch:
//
// - A session event, `privasys-connectors/set` {server, on}, records the
//   holder's choice in the session's own log, so it survives a restart and a
//   fork inherits it with the rest of the conversation.
// - A session projection, `connectors`, folds those events over the
//   deployment's defaults and is what the composer chip reads.
// - The `/connectors <server> on|off` command is the one write path; the chip
//   submits it as a command line, as dsh's own permission picker does.
// - Each agent of a session has the tools of the connectors switched off
//   there removed from its tool list (`agent.ctx.tools.restrict({deny})`), so
//   the model never sees them. A subagent follows the session it was started
//   from. Restrictions are agent-scoped and unwind with the agent.
//
// What a connector is, and its defaults, is the deployment's: the proxy
// composes this plugin per worker with the connectors it mounts. Nothing here
// names a product; the labels are the proxy's.

export const name = 'privasys-connectors'
export const inject = ['sessions', 'sessionProjections', 'tools']

const EVENT = 'privasys-connectors/set'
const KEY = 'connectors'
const SERVER = /^[a-z][a-z0-9_]*$/

/** The deployment's connectors, from the plugin config. */
function connectorsOf(config) {
  const out = []
  for (const c of Array.isArray(config?.connectors) ? config.connectors : []) {
    const server = String(c?.server ?? '')
    if (!SERVER.test(server) || out.some(x => x.server === server)) continue
    out.push({
      server,
      label: String(c?.label || server),
      category: String(c?.category || ''),
      detail: String(c?.detail || ''),
      on: c?.on !== false,
    })
  }
  return out
}

/** A schema is anything with parse(); the registry needs nothing more. */
const stateSchema = {
  parse(value) {
    if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new Error('connectors: state must be an object')
    const set = value.set
    if (set === null || typeof set !== 'object' || Array.isArray(set)) throw new Error('connectors: state.set must be an object')
    const out = {}
    for (const [k, v] of Object.entries(set)) {
      if (!SERVER.test(k) || typeof v !== 'boolean') throw new Error(`connectors: bad switch ${k}`)
      out[k] = v
    }
    return { set: out }
  },
}
const viewSchema = {
  parse(value) {
    if (value === null || typeof value !== 'object' || !Array.isArray(value.connectors)) throw new Error('connectors: bad view')
    return value
  },
}

export function apply(ctx, config) {
  const connectors = connectorsOf(config)
  const toolPrefix = server => `mcp__${server}__`

  // The session's own switches, over the deployment's defaults.
  const effective = (set) => {
    const out = {}
    for (const c of connectors) out[c.server] = Object.hasOwn(set, c.server) ? set[c.server] : c.on
    return out
  }

  ctx.effect(() => ctx.sessionProjections.register({
    key: KEY,
    stateVersion: 1,
    stateSchema,
    init: () => ({ set: {} }),
    apply(state, event) {
      if (event.type !== EVENT) return state
      const { server, on } = event.data ?? {}
      if (!SERVER.test(String(server)) || typeof on !== 'boolean') return state
      if (state.set[server] === on) return state
      return { set: { ...state.set, [server]: on } }
    },
    wire: {
      viewSchema,
      view: (state) => {
        const on = effective(state.set)
        return { connectors: connectors.map(c => ({ ...c, on: on[c.server] })) }
      },
    },
  }), 'privasys-connectors: projection')

  /** A session's switches, a subagent's taken from the session it came from. */
  const switchesOf = (session) => {
    const chain = []
    for (let s = session, depth = 0; s !== undefined && depth < 16; depth++) {
      chain.unshift(s)
      const parent = s.header?.parentSession
      s = parent === undefined ? undefined : ctx.sessions.get(parent)
    }
    let set = {}
    for (const s of chain) set = { ...set, ...(ctx.sessionProjections.stateOf(s, KEY)?.set ?? {}) }
    return effective(set)
  }

  // Each live agent's current restriction, lifted and redrawn on any change.
  const live = new Set()
  const lifts = new Map()
  const restrict = (agent) => {
    lifts.get(agent)?.()
    lifts.delete(agent)
    const on = switchesOf(agent.session)
    const off = connectors.filter(c => !on[c.server]).map(c => toolPrefix(c.server))
    if (off.length === 0) return
    const deny = ctx.tools.schemas(agent).map(t => t.name).filter(n => off.some(p => n.startsWith(p)))
    if (deny.length === 0) return
    try {
      lifts.set(agent, agent.ctx.tools.restrict({ deny }))
    } catch {
      // An agent already winding down rejects new registrations; nothing to hide.
    }
  }
  const restrictAll = () => { for (const agent of live) restrict(agent) }

  ctx.on('agent/created', ({ agent }) => {
    live.add(agent)
    restrict(agent)
  })
  ctx.on('agent/disposed', ({ agent }) => {
    live.delete(agent)
    lifts.delete(agent)
  })
  // A connector's tools arrive when its MCP server answers, often after the
  // agent exists: redraw so a late tool is hidden too.
  ctx.on('tools/change', restrictAll)
  ctx.on('session/event', (session, event) => {
    if (event.type !== EVENT) return
    for (const agent of live) {
      // The session itself and every subagent started from it.
      for (let s = agent.session, depth = 0; s !== undefined && depth < 16; depth++) {
        if (s === session) { restrict(agent); break }
        const parent = s.header?.parentSession
        s = parent === undefined ? undefined : ctx.sessions.get(parent)
      }
    }
  })

  // The one write path: `/connectors` lists, `/connectors <server> on|off` switches.
  ctx.inject(['commands'], (commandCtx) => {
    commandCtx.commands.register({
      definitionId: 'privasys-connectors',
      name: 'connectors',
      description: 'Choose which connectors this conversation may use',
      input: { hint: '<connector> on|off' },
      handler: ({ agent, rawInput }) => {
        const on = switchesOf(agent.session)
        const words = rawInput.trim().split(/\s+/).filter(Boolean)
        if (words.length === 0) {
          const list = connectors.map(c => `${c.label} ${on[c.server] ? 'on' : 'off'}`).join(', ')
          return { kind: 'success', text: list || 'no connectors on this deployment' }
        }
        const [server, value] = words
        const c = connectors.find(x => x.server === server)
        if (c === undefined) {
          return { kind: 'error', text: `unknown connector "${server}" (available: ${connectors.map(x => x.server).join(', ')})` }
        }
        if (value !== 'on' && value !== 'off') return { kind: 'error', text: 'say on or off' }
        if (on[server] !== (value === 'on')) agent.session.append(EVENT, { server, on: value === 'on' })
        return { kind: 'success', text: `${c.label} ${value}` }
      },
    })
  })
}
