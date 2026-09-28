/**
 * Privasys: the Connectors chip in the composer (attested-harness fork).
 *
 * Which of the deployment's attested connectors this conversation may use,
 * grouped by category, one switch each. It reads the host's `connectors`
 * session projection (app/privasys-connectors.mjs) and writes through the
 * host's `/connectors <server> on|off` command, the way dsh's own permission
 * picker reads its projection and writes through `/permission`: the pushed
 * projection frame is the one confirmation.
 *
 * Placed in the permission-presets client package because that package
 * already carries every dependency a composer control needs, and the image
 * installs before the overlay with a frozen lockfile. It names nothing: the
 * connectors, their labels and categories are the host's.
 */
import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { IconChevronDownOutlineRegular, Menu } from '@deepseek-ai/dsh-client-ui-primitives'
import type { MenuEntry } from '@deepseek-ai/dsh-client-ui-primitives'
import type { InjectFace, PropsRuntime } from '@deepseek-ai/dsh-client-ui-slots'
// Type-only: the conversation-owned composer slot declarations.
import type {} from '@deepseek-ai/dsh-client-ui-conversation/client'
import css from './PermissionSelect.module.css'

/** One connector as the host's projection shows it. */
interface Connector {
  server: string
  label: string
  category: string
  /** One line saying whose service it reaches ("Files in your Google Drive or OneDrive"). */
  detail?: string
  on: boolean
}

interface ConnectorsView {
  connectors: Connector[]
}

/**
 * Where a connector stands for the signed-in holder, from the harness
 * gateway (`/privasys/connectors/events`): whether their approval is there
 * for this conversation to use, and for which account.
 */
interface Standing {
  name: string
  state: 'approved' | 'not_approved' | 'declined' | 'open' | 'unknown'
  account?: string
  product?: string
}

// One sealed socket for the page, however many composers are open: the
// gateway pushes the holder's standing on every approval or revoke the
// runtime reports, and every 25 seconds as a keepalive. The page's
// transport paces a socket that cannot open, so a lost session costs
// nothing here.
let standings: ReadonlyMap<string, Standing> = new Map()
const listeners = new Set<() => void>()
let socket: WebSocket | null = null
let retry: ReturnType<typeof setTimeout> | null = null

function connect(): void {
  if (socket !== null || listeners.size === 0) return
  const ws = new WebSocket('/privasys/connectors/events')
  socket = ws
  ws.addEventListener('message', (event) => {
    try {
      const body = JSON.parse(String(event.data)) as { connectors?: Standing[] }
      standings = new Map((body.connectors ?? []).map(c => [c.name, c]))
      for (const listener of listeners) listener()
    } catch { /* a frame we cannot read changes nothing */ }
  })
  ws.addEventListener('close', () => {
    if (socket === ws) socket = null
    if (listeners.size > 0 && retry === null) {
      retry = setTimeout(() => { retry = null; connect() }, 5000)
    }
  })
}

function useStandings(): ReadonlyMap<string, Standing> {
  const [, setTick] = useState(0)
  useEffect(() => {
    const listener = (): void => { setTick(n => n + 1) }
    listeners.add(listener)
    connect()
    return () => {
      listeners.delete(listener)
      if (listeners.size === 0) {
        if (retry !== null) { clearTimeout(retry); retry = null }
        const ws = socket
        socket = null
        ws?.close()
      }
    }
  }, [])
  return standings
}

/** What to say under a connector's name: its account when approved, else why it cannot be used. */
function lineFor(c: Connector, s: Standing | undefined): { text?: string; warn: boolean } {
  switch (s?.state) {
    case 'approved':
      return s.account
        ? { text: s.product ? `${s.product} · ${s.account}` : s.account, warn: false }
        : { text: c.detail, warn: false }
    case 'not_approved':
      return { text: 'Not connected. Ask the agent to connect it, then approve on your phone.', warn: true }
    case 'declined':
      return { text: 'You declined access. Ask the agent if you change your mind.', warn: true }
    default:
      return { text: c.detail, warn: false }
  }
}

/** Whether a conversation that has a connector on can actually use it now. */
function usable(s: Standing | undefined): boolean {
  return s === undefined || s.state === 'approved' || s.state === 'open' || s.state === 'unknown'
}

/** Business face injected by the slot registration. */
export interface PrivasysConnectorsInjected {
  /** Switch one connector for this session through the host command. */
  toggle: (server: string, on: boolean) => Promise<boolean>
}

export type PrivasysConnectorsProps =
  PropsRuntime<'conversation.input.left'>
  & InjectFace<PrivasysConnectorsInjected>

export function PrivasysConnectors(props: PrivasysConnectorsProps) {
  const { toggle } = props
  // The projection key is the host plugin's, not in dsh's typed map.
  const useProjection = (props as unknown as { useProjection: (key: string) => unknown }).useProjection
  const view = useProjection('connectors') as ConnectorsView | undefined
  const live = useStandings()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)

  if (view === undefined || view.connectors.length === 0) return null
  const connectors = view.connectors
  const on = connectors.filter(c => c.on)
  const ready = on.filter(c => usable(live.get(c.server)))

  // One heading per category, in the order the host lists them.
  const groups: { category: string; connectors: Connector[] }[] = []
  for (const c of connectors) {
    const g = groups.find(x => x.category === c.category)
    if (g === undefined) groups.push({ category: c.category, connectors: [c] })
    else g.connectors.push(c)
  }
  const items: MenuEntry[] = []
  for (const g of groups) {
    if (g.category !== '') items.push({ type: 'label', id: `category:${g.category}`, text: g.category })
    for (const c of g.connectors) {
      const line = lineFor(c, live.get(c.server))
      items.push({
        id: c.server,
        label: (
          <span style={{ display: 'flex', flexDirection: 'column' }}>
            <span style={usable(live.get(c.server)) ? undefined : { opacity: 0.6 }}>{c.label}</span>
            {line.text
              ? (
                <span style={{ fontSize: '0.85em', opacity: line.warn ? 0.9 : 0.65, color: line.warn ? '#b45309' : undefined }}>
                  {line.text}
                </span>
              )
              : null}
          </span>
        ),
        disabled: busy !== null,
      })
    }
  }

  const choose = (server: string): void => {
    const c = connectors.find(x => x.server === server)
    if (c === undefined || busy !== null) return
    setBusy(server)
    void toggle(server, !c.on)
      .catch(() => false)
      .then(() => { setBusy(null) })
  }

  // Counted as what this conversation can use now: switched on, and approved.
  const label = `Connectors ${ready.length}/${connectors.length}`
  return (
    <Menu
      open={open}
      items={items}
      selectedIds={on.map(c => c.server)}
      selection="check"
      onSelect={choose}
      onClose={() => { setOpen(false) }}
      side="top"
      portal
      anchor={
        <button
          type="button"
          className={css.trigger}
          aria-label={label}
          title="Which connectors this conversation may use"
          disabled={busy !== null}
          onClick={() => { setOpen(!open) }}
        >
          <span className={css.triggerLabel}>{label}</span>
          <span className={clsx(css.chevron, open && css.chevronOpen)} aria-hidden>
            <IconChevronDownOutlineRegular />
          </span>
        </button>
      }
    />
  )
}
