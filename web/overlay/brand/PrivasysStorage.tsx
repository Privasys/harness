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
        // The ask is over once the runtime reports the grant: drop the
        // "approve on your device" box rather than leaving the user on it.
        if (d?.persistent === true) setAsk(undefined)
      }, () => { setState({}) })
  }
  // Poll: quickly until the session is bound to someone, then slowly, so a
  // grant approved on the device (or withdrawn in Drive) shows without a
  // reload. The panel opening also re-checks.
  useEffect(() => {
    refresh()
    const t = setInterval(refresh, state === undefined ? 3000 : 30000)
    return () => { clearInterval(t) }
  }, [state === undefined])
  useEffect(() => { if (open) refresh() }, [open])

  const request = (retry: boolean): void => {
    setBusy(true)
    void pvFetch(`/privasys/capability/request${retry ? '?retry=1' : ''}`, { method: 'POST' })
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => { setAsk(d); setBusy(false); refresh() },
        () => { setBusy(false) })
  }

  if (state === undefined) return null

  // A grant the runtime recorded but Drive now refuses (withdrawn there, or
  // expired) is NOT "saved": the mirror sees the refusal and the row says so.
  const withdrawn = state.withdrawn === true
  const persistent = state.persistent === true && !withdrawn
  const declined = state.declined === true
  // Not an option with two acceptable answers. Without a Drive this harness
  // cannot keep anything: the session root is a tmpfs and dies with the
  // container. The row says setup is incomplete, not that a preference is unset.
  const dot = persistent ? 'done' as const : withdrawn ? 'error' as const : 'warning' as const
  const word = persistent ? 'In your Drive' : withdrawn ? 'Withdrawn' : 'Not saved'
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
              <p>
                Your conversations and workspace are saved to <strong>{folder}</strong> in
                your own Drive, under your own keys. They survive this enclave being
                replaced. You can withdraw access at any time in Drive.
              </p>
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
                  <Button variant="primary" size="sm" disabled={busy} onClick={() => { request(declined || withdrawn) }}>
                    {busy ? 'Preparing…' : withdrawn ? 'Connect again' : declined ? 'Ask me again' : 'Connect my Drive'}
                  </Button>
                </div>
              </>
            )}

          {ask?.nonce && !persistent
            ? (
              <div className={css.askBox}>
                <div className={css.askTitle}>Approve on your device</div>
                <p className={css.muted}>
                  Your wallet verifies this enclave itself and shows you exactly what is
                  being asked for. A notification is on its way to your device; if it does
                  not arrive, enter these in the wallet by hand.
                </p>
                <div className={css.askFacts}>
                  <div><span className={css.muted}>host&nbsp;&nbsp;</span><code>{ask.app_host}</code></div>
                  <div><span className={css.muted}>nonce&nbsp;</span><code>{ask.nonce}</code></div>
                </div>
                <div className={css.actions}>
                  <Button variant="outline" size="sm" onClick={refresh}>I have approved it</Button>
                </div>
              </div>
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
