/**
 * "Where your sessions are kept" — the storage row, and the one place a user
 * can ask for their conversations to live in their own Drive.
 *
 * Two jobs, both required by the harness-side commitments in
 * plans/harness-drive-grant-wallet.md:
 *
 *   4. Show a non-blocking banner while no capability exists: sessions are
 *      being kept in memory only and will not persist. Stop asking on denial.
 *   2/3. Start the flow. The harness generates a nonce; the push carries only
 *      that nonce and this host, and everything else the wallet learns from the
 *      attested fetch.
 *
 * NOT an option with two acceptable answers. The harness enclave holds no user
 * data: the session root is a tmpfs that dies with the container, so without a
 * Drive nothing is kept at all. The row says SETUP IS INCOMPLETE, never that a
 * preference is unset — an earlier version of this file offered "not saved to
 * your Drive" as though staying in the enclave were a legitimate resting place,
 * which inverted the whole data model.
 *
 * Still a sidebar row rather than a modal: non-blocking was the commitment, and
 * nobody mid-task should be interrupted. It states the current truth at all
 * times, so "where are my sessions?" is a glance rather than a support
 * question. The row is dsh's foot control (PrivasysFootRow.tsx) with the
 * state as a dot and a word; the explainer is dsh's Modal.
 */
import { useEffect, useState } from 'react'
import { Button, IconFolderOpenOutline16, Modal, StateDot } from '@deepseek-ai/dsh-client-ui-primitives'
import type { SidebarFooterActionOwnerProps } from '@deepseek-ai/dsh-client-ui-sidebar/client'
import { FootRow } from './PrivasysFootRow.tsx'
import css from './PrivasysFoot.module.css'

interface StorageState {
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

interface PendingAsk {
  status?: string
  nonce?: string
  app_host?: string
}

// Both storage calls MUST ride the sealed transport the shell installs
// (window.__DSH_TRANSPORT__.fetch): the acting subject the proxy files the
// capability under is the relay-asserted X-Privasys-Sub, which the relay sets
// only on sealed requests and strips from everything else, and the relay
// refuses a plaintext POST on the terminate leg outright (403
// sealed-transport-required). Off-platform there is no sealed session and the
// raw fetch is the right call.
function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

export function PrivasysStorageRow({ wide }: SidebarFooterActionOwnerProps) {
  const [state, setState] = useState<StorageState | undefined>(undefined)
  const [ask, setAsk] = useState<PendingAsk | undefined>(undefined)
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)
  // Set while an ask is on its way to the device: the row says so and the
  // button stays down (one tap sends one push; a second click sent a second
  // notification, 2026-09-16). The answer arrives through the held request
  // below the moment the wallet delivers it; nothing polls.
  const [waitingSince, setWaitingSince] = useState<number | undefined>(undefined)
  const waiting = waitingSince !== undefined

  const refresh = (): void => {
    void pvFetch('/privasys/capability/status')
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => {
        // An answer about nobody (the sealed session is still anonymous
        // right after sign-in) is not an answer: keep the row hidden and
        // ask again shortly rather than showing "Connect your Drive" to a
        // user whose Drive is connected.
        if (d !== undefined && d.signed_in === false) {
          setState(undefined)
          return
        }
        setState(d ?? {})
        // The ask is over once the runtime reports the grant and Drive
        // honours it: drop the "approve on your device" box rather than
        // leaving the user on it.
        if (d?.persistent === true && d?.stale !== true && d?.withdrawn !== true) {
          setAsk(undefined)
          setWaitingSince(undefined)
        }
      }, () => { setState({}) })
  }
  // Pushed, never polled: once the session is bound to someone, the row
  // holds one sealed WebSocket on which the proxy writes the status
  // whenever something about this holder changes (the runtime's event
  // stream) and every 25 seconds as a keepalive. A WebSocket, not a held
  // request: the sealed transport answers unary calls one at a time, in
  // order, so a request held for 25 seconds queued every dsh call behind it
  // (workspace actions took 25 seconds each, 2026-09-18). Until the session
  // is bound the proxy has nobody to push about, so that first phase
  // re-asks every few seconds. The panel opening also re-checks.
  useEffect(() => {
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let ws: WebSocket | undefined
    const apply = (d: (StorageState & { signed_in?: boolean }) | undefined): boolean => {
      if (d !== undefined && d.signed_in === false) {
        setState(undefined)
        return false
      }
      setState(d ?? {})
      if (d?.persistent === true && d?.stale !== true && d?.withdrawn !== true) {
        setAsk(undefined)
        setWaitingSince(undefined)
      }
      return true
    }
    if (state === undefined) {
      const loop = (): void => {
        if (stopped) return
        void pvFetch('/privasys/capability/status')
          .then(async r => (r.ok ? await r.json() : undefined))
          .then(d => {
            if (stopped) return
            apply(d)
            timer = setTimeout(loop, 3000)
          }, () => {
            if (stopped) return
            setState({})
            timer = setTimeout(loop, 5000)
          })
      }
      loop()
      return () => { stopped = true; if (timer !== undefined) clearTimeout(timer) }
    }
    // The shell routes this URL through the sealed session (privasys-shell.js);
    // off-platform there is no proxy behind it and the socket simply fails,
    // in which case the row keeps what it last knew and retries slowly.
    const listen = (): void => {
      if (stopped) return
      try {
        ws = new WebSocket('/privasys/capability/events')
      } catch {
        timer = setTimeout(listen, 15000)
        return
      }
      ws.addEventListener('message', ev => {
        if (stopped) return
        try { apply(JSON.parse(String(ev.data))) } catch { /* not ours */ }
      })
      ws.addEventListener('close', () => {
        if (stopped) return
        timer = setTimeout(listen, 5000)
      })
    }
    listen()
    return () => {
      stopped = true
      if (timer !== undefined) clearTimeout(timer)
      try { ws?.close() } catch { /* ignore */ }
    }
  }, [state === undefined])
  useEffect(() => { if (open) refresh() }, [open])
  // A notification dismissed by mistake is gone from the phone: after 45
  // seconds the button comes back as "Send it again" (2026-09-19: three
  // minutes read as a hang). The answer itself arrives on the events socket
  // whenever it comes; the ask stays valid on the runtime for its own life.
  useEffect(() => {
    if (waitingSince === undefined) return undefined
    const t = setTimeout(() => { setWaitingSince(undefined) }, 45 * 1000)
    return () => { clearTimeout(t) }
  }, [waitingSince])

  const request = (retry: boolean): void => {
    if (busy || waiting) return
    setBusy(true)
    void pvFetch(`/privasys/capability/request${retry ? '?retry=1' : ''}`, { method: 'POST' })
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => {
        setAsk(d)
        setBusy(false)
        // Only a push that went out is worth waiting for; "already granted"
        // or "declined" answers settle at once.
        if (d?.nonce) setWaitingSince(Date.now())
        refresh()
      }, () => { setBusy(false) })
  }

  if (state === undefined) return null

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
  const dot = waiting ? 'warning' as const : stale ? 'warning' as const : persistent ? 'done' as const : withdrawn ? 'error' as const : 'warning' as const
  const word = waiting ? 'Waiting for your device' : stale ? 'Approval needed' : persistent ? 'In your Drive' : withdrawn ? 'Withdrawn' : 'Not saved'
  const folder = state.folder ?? 'AppData/Harness'

  return (
    <>
      <FootRow
        wide={wide}
        icon={<IconFolderOpenOutline16 size={wide ? 16 : 18} />}
        label="Sessions"
        status={<><StateDot state={dot} size={10} /><span>{word}</span></>}
        title={persistent
          ? 'Your sessions are stored in your own Drive'
          : 'This harness cannot keep your sessions until you connect your Drive'}
        ariaLabel="Session storage"
        haspopup="dialog"
        expanded={open}
        onClick={() => { setOpen(true) }}
      />
      <Modal
        open={open}
        onClose={() => { setOpen(false) }}
        title="Where your sessions are kept"
        closeLabel="Close"
        className={css.storageDialog ?? ''}
      >
        <div className={css.dialogBody}>
          {persistent
            ? (
              <>
                <p>
                  Your conversations and workspace are saved to <strong>{folder}</strong> in
                  your own Drive, under your own keys. They survive this enclave being
                  replaced. You can withdraw access at any time in Drive.
                </p>
                {stale
                  ? (
                    <>
                      <p className={css.warn}>
                        This harness now asks for <strong>{(missing.length > 0 ? missing : state.permissions ?? []).join(', ')}</strong> on
                        that folder (so it can remove sessions you delete and tidy old copies).
                        What you approved earlier keeps working until you answer; approve the
                        updated access on your device to enable it.
                      </p>
                      <div className={css.actions}>
                        <Button variant="primary" size="sm" disabled={busy || waiting} onClick={() => { request(false) }}>
                          {busy ? 'Preparing…' : waiting ? 'Sent to your device…' : ask?.nonce ? 'Send it again' : 'Approve the updated access'}
                        </Button>
                      </div>
                    </>
                  )
                  : null}
              </>
            )
            : (
              <>
                <p>
                  This harness keeps <strong>no copy of your data</strong>. Your
                  conversations are held in memory for as long as this enclave runs and
                  are gone the moment it stops — that is deliberate, not a limitation to
                  work around.
                </p>
                <p>
                  To keep them, connect your Drive. They are then stored
                  under <strong>{folder}</strong> in <em>your</em> Drive, under your own
                  keys, where this harness can reach that one folder and nothing else —
                  and you can withdraw it at any time in Drive.
                </p>
                {withdrawn
                  ? (
                    <p className={css.warn}>
                      Your Drive is refusing this harness: the access was withdrawn there, or
                      has expired. Nothing has been saved since. Connect your Drive again to
                      approve it afresh.
                    </p>
                  )
                  : null}
                {declined
                  ? <p className={css.muted}>You declined this earlier, so you are not being asked again.</p>
                  : null}
                <div className={css.actions}>
                  <Button variant="primary" size="sm" disabled={busy || waiting} onClick={() => { request(declined || withdrawn) }}>
                    {busy ? 'Preparing…' : waiting ? 'Sent to your device…' : ask?.nonce ? 'Send it again' : withdrawn ? 'Connect again' : declined ? 'Ask me again' : 'Connect my Drive'}
                  </Button>
                </div>
              </>
            )}

          {ask?.nonce && (!persistent || stale)
            ? (
              <p className={css.muted}>
                Your wallet verifies this enclave itself and shows you exactly what is
                being asked for. Nothing to do here: this row updates the moment you
                answer on your device.
              </p>
            )
            : null}
          {ask?.status === 'declined'
            ? <p className={css.muted}>Still declined.</p>
            : null}
        </div>
      </Modal>
    </>
  )
}
