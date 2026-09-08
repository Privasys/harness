// @ts-nocheck -- same vendored-interop reason as PrivasysAttestation.tsx.
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
 * question.
 */
import { useEffect, useState } from 'react'
import type { SidebarFooterActionOwnerProps } from '@deepseek-ai/dsh-client-ui-sidebar/client'
import { ensureRowStyles } from './PrivasysRows.tsx'

interface StorageState {
  persistent?: boolean
  declined?: boolean
  folder?: string
}

interface PendingAsk {
  status?: string
  nonce?: string
  app_host?: string
}

function DriveIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none"
      stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"
      aria-hidden="true">
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </svg>
  )
}

export function PrivasysStorageRow({ wide }: SidebarFooterActionOwnerProps) {
  useEffect(() => { ensureRowStyles() }, [])
  const [state, setState] = useState<StorageState | undefined>(undefined)
  const [ask, setAsk] = useState<PendingAsk | undefined>(undefined)
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)

  const refresh = (): void => {
    void fetch('/privasys/capability/status')
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => { setState(d ?? {}) }, () => { setState({}) })
  }
  useEffect(refresh, [])

  const request = (retry: boolean): void => {
    setBusy(true)
    void fetch(`/privasys/capability/request${retry ? '?retry=1' : ''}`, { method: 'POST' })
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => { setAsk(d); setBusy(false); refresh() },
        () => { setBusy(false) })
  }

  if (state === undefined) return null

  const persistent = state.persistent === true
  const declined = state.declined === true
  // Not an option with two acceptable answers. Without a Drive this harness
  // cannot keep anything: the session root is a tmpfs and dies with the
  // container. The row says setup is incomplete, not that a preference is unset.
  const label = persistent ? 'Saved to your Drive' : 'Connect your Drive'
  const colour = persistent ? '#059669' : '#d97706' 

  return (
    <>
      <button
        type="button"
        className={`pv-row${wide ? '' : ' pv-row-narrow'}`}
        style={{ color: colour }}
        title={persistent
          ? 'Your sessions are stored in your own Drive'
          : 'This harness cannot keep your sessions until you connect your Drive'}
        aria-label="Session storage"
        onClick={() => { setOpen(true) }}
      >
        <DriveIcon size={wide ? 16 : 18} />
        {wide
          ? <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{label}</span>
          : null}
      </button>
      {open
        ? (
          <div className="pv-att-overlay" onClick={() => { setOpen(false) }}>
            <div className="pv-att-panel pv-att-scope" role="dialog" aria-label="Session storage"
              style={{ maxWidth: 560 }} onClick={(e) => { e.stopPropagation() }}>
              <div className="pv-att-head">
                <h1 className="pv-att-title">Where your sessions are kept</h1>
                <button type="button" className="pv-att-close" aria-label="Close"
                  onClick={() => { setOpen(false) }}>×</button>
              </div>

              {persistent
                ? (
                  <p style={{ fontSize: 13.5 }}>
                    Your conversations and workspace are saved to <strong>{state.folder ?? 'AppData/Harness'}</strong> in
                    your own Drive, under your own keys. They survive this enclave being
                    replaced. You can withdraw access at any time in Drive.
                  </p>
                )
                : (
                  <>
                    <p style={{ fontSize: 13.5 }}>
                      This harness keeps <strong>no copy of your data</strong>. Your
                      conversations are held in memory for as long as this enclave runs and
                      are gone the moment it stops — that is deliberate, not a limitation to
                      work around.
                    </p>
                    <p style={{ fontSize: 13.5 }}>
                      To keep them, connect your Drive. They are then stored
                      under <strong>{state.folder ?? 'AppData/Harness'}</strong> in{' '}
                      <em>your</em> Drive, under your own keys, where this harness can reach
                      that one folder and nothing else — and you can withdraw it at any time
                      in Drive.
                    </p>
                    {declined
                      ? (
                        <p style={{ fontSize: 12.5, color: '#6b7280' }}>
                          You declined this earlier, so you are not being asked again.
                        </p>
                      )
                      : null}
                    <button type="button" className="pv-row" style={{ width: 'auto', marginTop: 8 }}
                      disabled={busy}
                      onClick={() => { request(declined) }}>
                      {busy ? 'Preparing…' : declined ? 'Ask me again' : 'Connect my Drive'}
                    </button>
                  </>
                )}

              {ask?.nonce
                ? (
                  <div style={{ marginTop: 14, border: '1px solid #e5e7eb', borderRadius: 8, padding: '10px 12px' }}>
                    <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 4 }}>Approve on your device</div>
                    <p style={{ fontSize: 12.5, color: '#6b7280', margin: '0 0 8px' }}>
                      Your wallet verifies this enclave itself and shows you exactly what is
                      being asked for. A notification is on its way to your device; if it does
                      not arrive, enter these in the wallet by hand.
                    </p>
                    <div style={{ fontSize: 12 }}>
                      <div><span style={{ color: '#6b7280' }}>host&nbsp;&nbsp;</span><code>{ask.app_host}</code></div>
                      <div style={{ marginTop: 4, wordBreak: 'break-all' }}>
                        <span style={{ color: '#6b7280' }}>nonce&nbsp;</span><code>{ask.nonce}</code>
                      </div>
                    </div>
                    <button type="button" className="pv-row" style={{ width: 'auto', marginTop: 10 }}
                      onClick={refresh}>I have approved it</button>
                  </div>
                )
                : null}
              {ask?.status === 'declined'
                ? <p style={{ fontSize: 12.5, color: '#6b7280' }}>Still declined.</p>
                : null}
            </div>
          </div>
        )
        : null}
    </>
  )
}
