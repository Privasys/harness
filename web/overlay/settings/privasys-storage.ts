/**
 * Where this holder's conversations are kept: one store for the page, read by
 * the Storage settings page and by the sidebar row that shows only while
 * setup is incomplete (attested-harness fork).
 *
 * Pushed, never polled. The store holds one sealed WebSocket on
 * /privasys/capability/events, on which the proxy writes the status at once
 * and again whenever something about this holder changes (the runtime's event
 * stream), with a 25-second keepalive that carries the current truth. A
 * WebSocket, not a held request: the sealed transport answers unary calls one
 * at a time, in order, so a request held for 25 seconds queued every dsh call
 * behind it (2026-09-18). The proxy refuses the socket while the sealed
 * session is still anonymous (right after sign-in); a refused or dropped
 * socket is dialled again after a growing pause, which is a reconnect, not a
 * status poll.
 *
 * Every call MUST ride the sealed transport the shell installs
 * (window.__DSH_TRANSPORT__.fetch): the proxy files the capability under the
 * relay-asserted X-Privasys-Sub, which the relay sets only on sealed requests.
 * Off-platform there is no sealed session and the raw fetch is the right call.
 */

export interface StorageState {
  /** The holder's current worker (its start time), "" while none runs. */
  worker?: string
  persistent?: boolean
  declined?: boolean
  signed_in?: boolean
  withdrawn?: boolean
  folder?: string
  /** Approved under other permissions than this harness now declares. */
  stale?: boolean
  permissions?: string[]
  granted_permissions?: string[]
}

export interface PendingAsk {
  status?: string
  nonce?: string
  app_host?: string
}

export interface StorageView {
  /** Undefined until the proxy has answered about a signed-in holder. */
  state: StorageState | undefined
  ask: PendingAsk | undefined
  busy: boolean
  /** An ask is on its way to the device: one tap sends one push. */
  waiting: boolean
  /** The working-files folder re-ask: sent, already granted, or failed. */
  folderAsk: 'sent' | 'granted' | 'failed' | undefined
}

/** What the page shows about the storage, derived once for every reader. */
export interface StorageSummary {
  persistent: boolean
  withdrawn: boolean
  declined: boolean
  stale: boolean
  /** Setup is complete: conversations are written to the holder's Drive. */
  healthy: boolean
  dot: 'done' | 'warning' | 'error'
  word: string
  folder: string
  missing: string[]
}

export function summarise(view: StorageView): StorageSummary | undefined {
  const state = view.state
  if (state === undefined) return undefined
  // A grant the runtime recorded but Drive now refuses (withdrawn there, or
  // expired) is NOT "saved": the mirror sees the refusal and the row says so.
  const withdrawn = state.withdrawn === true
  const persistent = state.persistent === true && !withdrawn
  const declined = state.declined === true
  // Approved, working, but under a narrower (or other) permission set than
  // this harness now asks for: saved, and still needing one more approval.
  const stale = persistent && state.stale === true
  const missing = (state.permissions ?? []).filter(p => !(state.granted_permissions ?? []).includes(p))
  // Not an option with two acceptable answers. Without a Drive this harness
  // cannot keep anything: the session root is a tmpfs and dies with the
  // container. The row says setup is incomplete, not that a preference is unset.
  const dot = view.waiting || stale ? 'warning' : persistent ? 'done' : withdrawn ? 'error' : 'warning'
  const word = view.waiting ? 'Waiting for your device'
    : stale ? 'Approval needed'
      : persistent ? 'In your Drive'
        : withdrawn ? 'Withdrawn' : 'Not in your Drive'
  return {
    persistent, withdrawn, declined, stale,
    healthy: persistent && !stale && !view.waiting,
    dot, word,
    folder: state.folder ?? 'AppData/Harness',
    missing,
  }
}

function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

/** Pauses before dialling the socket again, the last one repeating. */
const REDIAL_MS = [1000, 2000, 5000, 15000, 30000]

/** A notification dismissed by mistake is gone from the phone: after 45
 * seconds the button comes back as "Send it again" (2026-09-19: three
 * minutes read as a hang). The answer itself arrives on the socket whenever
 * it comes; the ask stays valid on the runtime for its own life. */
const WAITING_MS = 45 * 1000

let view: StorageView = { state: undefined, ask: undefined, busy: false, waiting: false, folderAsk: undefined }
const listeners = new Set<() => void>()
let started = false
let waitingTimer: ReturnType<typeof setTimeout> | undefined
// The worker this page booted against. dsh reads its workspaces once at boot
// and binds its services to that process: when the harness replaces the
// worker (a Drive withdrawal, a fresh approval) the page reloads itself,
// instead of showing a stale sidebar and "active Service is unavailable"
// until someone presses refresh (2026-09-19).
let worker: string | undefined

function set(patch: Partial<StorageView>): void {
  view = { ...view, ...patch }
  for (const l of listeners) l()
}

function setWaiting(on: boolean): void {
  if (waitingTimer !== undefined) clearTimeout(waitingTimer)
  waitingTimer = on ? setTimeout(() => { waitingTimer = undefined; set({ waiting: false }) }, WAITING_MS) : undefined
  set({ waiting: on })
}

function apply(d: StorageState | undefined): void {
  const w = d?.worker
  if (w !== undefined && w !== '') {
    if (worker === undefined) worker = w
    else if (worker !== w) { worker = w; location.reload(); return }
  }
  // An answer about nobody (the sealed session is still anonymous right
  // after sign-in) is not an answer: show nothing rather than "Connect your
  // Drive" to a user whose Drive is connected.
  if (d !== undefined && d.signed_in === false) { set({ state: undefined }); return }
  set({ state: d ?? {} })
  // The ask is over once the runtime reports the grant and Drive honours
  // it: drop the "approve on your device" box rather than leaving the user
  // on it.
  if (d?.persistent === true && d?.stale !== true && d?.withdrawn !== true) {
    set({ ask: undefined })
    setWaiting(false)
  }
}

function listen(attempt: number): void {
  let ws: WebSocket
  let heard = false
  const again = (): void => {
    const next = heard ? 0 : attempt + 1
    setTimeout(() => { listen(next) }, REDIAL_MS[Math.min(next, REDIAL_MS.length - 1)])
  }
  try {
    // The shell routes this URL through the sealed session (privasys-shell.js).
    ws = new WebSocket('/privasys/capability/events')
  } catch {
    again()
    return
  }
  ws.addEventListener('message', ev => {
    heard = true
    try { apply(JSON.parse(String(ev.data)) as StorageState) } catch { /* not ours */ }
  })
  ws.addEventListener('close', again)
}

function start(): void {
  if (started) return
  started = true
  listen(0)
}

/** Subscribe to the store; the first subscriber opens the socket. */
export function subscribe(listener: () => void): () => void {
  start()
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function snapshot(): StorageView {
  return view
}

/** Read once, on a user opening the page: the socket carries every change. */
export function refresh(): void {
  void pvFetch('/privasys/capability/status')
    .then(async r => (r.ok ? await r.json() as StorageState : undefined))
    .then(apply, () => undefined)
}

/** Ask the device for the Drive folder. `retry` reopens a refusal: the user
 * changing their mind, never the app trying again. */
export function request(retry: boolean): void {
  if (view.busy || view.waiting) return
  set({ busy: true })
  void pvFetch(`/privasys/capability/request${retry ? '?retry=1' : ''}`, { method: 'POST' })
    .then(async r => (r.ok ? await r.json() as PendingAsk : undefined))
    .then(d => {
      set({ ask: d, busy: false })
      // Only a push that went out is worth waiting for; "already granted"
      // or "declined" answers settle at once.
      if (d?.nonce) setWaiting(true)
      refresh()
    }, () => { set({ busy: false }) })
}

/** The working-files folder: approved once on the device, standing until
 * revoked there. Asking again re-sends that approval, for a wallet that lost
 * the record (an approval it could not report), or a new phone. */
export function askFolderAgain(): void {
  if (view.busy) return
  set({ busy: true })
  void pvFetch('/privasys/capability/request?kind=app_storage&retry=1', { method: 'POST' })
    .then(async r => (r.ok ? await r.json() as PendingAsk : undefined))
    .then(d => {
      set({ busy: false, folderAsk: d?.nonce ? 'sent' : d?.status === 'already_granted' ? 'granted' : 'failed' })
    }, () => { set({ busy: false, folderAsk: 'failed' }) })
}
