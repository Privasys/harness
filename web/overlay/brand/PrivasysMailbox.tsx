/**
 * "Your mailbox" — the mail row, and the one place a user connects their own
 * mailbox to this harness.
 *
 * The second declared resource after storage, and deliberately a separate
 * component rather than a mode of PrivasysStorage: the storage status is
 * Drive-shaped (a folder, a withdrawal the mirror noticed) and none of that
 * means anything for a mailbox. This row reads the proxy's GENERIC resource
 * surface (capability_resources.go) and phrases it for mail.
 *
 * Two steps, in this order, because the connector enforces the order: it
 * refuses to mint a capability over a mailbox that was never linked (412 "this
 * holder has not linked a mailbox yet"). Linking happens on the connector's own
 * page, under the connector's own attestation, so the mailbox password never
 * passes through this harness. Allowing happens in the wallet, exactly like
 * the Drive grant.
 *
 * Rendered only where this fleet has a mail connector (a `mail` entry in the
 * proxy's tool hosts) and the measured manifest declares the resource; on a
 * fleet without one there is nothing to connect and the row stays away.
 */
import { useEffect, useState } from 'react'
import { Button, Modal, StateDot } from '@deepseek-ai/dsh-client-ui-primitives'
import type { SidebarFooterActionOwnerProps } from '@deepseek-ai/dsh-client-ui-sidebar/client'
import { FootRow } from './PrivasysFootRow.tsx'
import css from './PrivasysFoot.module.css'

const RESOURCE = 'mailbox'

interface MailboxState {
  approved?: boolean
  declined?: boolean
  signed_in?: boolean
  service_result?: { account?: string }
}

interface PendingAsk {
  status?: string
  nonce?: string
  app_host?: string
}

/** Envelope glyph on dsh's 16-grid, at the icon set's ~1.3px weight. */
function EnvelopeIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true">
      <rect x="1.9" y="3.4" width="12.2" height="9.2" rx="1.4" stroke="currentColor" strokeWidth="1.3" />
      <path d="m2.4 4.4 5.6 4.3 5.6-4.3" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

// Same rule as the storage row: capability calls ride the sealed transport,
// because the holder the proxy files the ask under is the relay-asserted
// X-Privasys-Sub, which exists only on sealed requests.
function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

export function PrivasysMailboxRow({ wide }: SidebarFooterActionOwnerProps) {
  const [mailHost, setMailHost] = useState<string | null | undefined>(undefined)
  const [state, setState] = useState<MailboxState | undefined>(undefined)
  const [ask, setAsk] = useState<PendingAsk | undefined>(undefined)
  const [error, setError] = useState<string | undefined>(undefined)
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)

  // Which connector, if any. The proxy's public summary is the source: it is
  // the same table the tool legs dial, so the row can never point at a
  // different host than the one the agent's mail tools use.
  useEffect(() => {
    let current = true
    void fetch('/privasys/attestation')
      .then(async r => (r.ok ? await r.json() : undefined))
      .then((doc: { tool_hosts?: Record<string, string> } | undefined) => {
        if (current) setMailHost(doc?.tool_hosts?.mail ?? null)
      }, () => { if (current) setMailHost(null) })
    return () => { current = false }
  }, [])

  const refresh = (): void => {
    void pvFetch(`/privasys/capability/${RESOURCE}/status`)
      .then(async r => {
        // 404 = the manifest declares no mailbox: nothing to show.
        if (r.status === 404) return null
        return r.ok ? await r.json() : undefined
      })
      .then((d: MailboxState | null | undefined) => {
        if (d === null) { setMailHost(null); return }
        // An answer about nobody (the sealed session is still anonymous just
        // after sign-in) is not an answer: stay hidden and ask again shortly.
        if (d !== undefined && d.signed_in === false) { setState(undefined); return }
        setState(d ?? {})
        if (d?.approved === true) { setAsk(undefined); setError(undefined) }
      }, () => { setState({}) })
  }
  useEffect(() => {
    if (mailHost === undefined || mailHost === null) return
    refresh()
    const t = setInterval(refresh, state === undefined ? 3000 : 30000)
    return () => { clearInterval(t) }
  }, [mailHost, state === undefined])
  useEffect(() => { if (open && mailHost) refresh() }, [open])

  const request = (retry: boolean): void => {
    setBusy(true)
    setError(undefined)
    void pvFetch(`/privasys/capability/${RESOURCE}/request${retry ? '?retry=1' : ''}`, { method: 'POST' })
      .then(async r => {
        const body = await r.json().catch(() => ({}))
        if (!r.ok) throw new Error(typeof body?.error === 'string' ? body.error : `HTTP ${r.status}`)
        return body as PendingAsk
      })
      .then(d => { setAsk(d); setBusy(false); refresh() },
        (e: Error) => { setError(e.message); setBusy(false) })
  }

  if (!mailHost || state === undefined) return null

  const approved = state.approved === true
  const declined = state.declined === true
  const account = state.service_result?.account
  const linkUrl = `https://${mailHost}/`

  return (
    <>
      <FootRow
        wide={wide}
        icon={<EnvelopeIcon size={wide ? 16 : 18} />}
        label="Mail"
        status={<><StateDot state={approved ? 'done' : 'warning'} size={10} /><span>{approved ? 'Connected' : 'Not connected'}</span></>}
        title={approved
          ? `The agent can read and draft in ${account ?? 'your mailbox'}`
          : 'Connect your mailbox so the agent can read and draft replies'}
        ariaLabel="Mailbox"
        haspopup="dialog"
        expanded={open}
        onClick={() => { setOpen(true) }}
      />
      <Modal
        open={open}
        onClose={() => { setOpen(false) }}
        title="Your mailbox"
        closeLabel="Close"
        className={css.storageDialog ?? ''}
      >
        <div className={css.dialogBody}>
          {approved
            ? (
              <p>
                The agent can read <strong>{account ?? 'your mailbox'}</strong> and prepare
                drafts in it. It never sends: every reply waits in your Drafts folder for
                you. Your mailbox password stays sealed in your own Drive, and you can
                disconnect or withdraw this access at any time.
              </p>
            )
            : (
              <>
                <p>
                  Connecting takes two steps, in this order.
                </p>
                <p>
                  <strong>1. Link your mailbox</strong> on the mail connector&apos;s own page.
                  Your password goes to that attested service and is sealed in your own
                  Drive; it never passes through this harness.
                </p>
                <div className={css.actions}>
                  <Button variant="outline" size="sm" onClick={() => { window.open(linkUrl, '_blank', 'noopener') }}>
                    Open the mail connector
                  </Button>
                </div>
                <p>
                  <strong>2. Allow this harness</strong> to use it. Your wallet verifies both
                  enclaves and shows you exactly what is being asked for: reading your mail
                  and preparing drafts, never sending.
                </p>
                {declined
                  ? <p className={css.muted}>You declined this earlier, so you are not being asked again.</p>
                  : null}
                <div className={css.actions}>
                  <Button variant="primary" size="sm" disabled={busy} onClick={() => { request(declined) }}>
                    {busy ? 'Preparing…' : declined ? 'Ask me again' : 'Allow on my device'}
                  </Button>
                </div>
                {error !== undefined
                  ? <p className={css.warn}>{error}</p>
                  : null}
              </>
            )}

          {ask?.nonce && !approved
            ? (
              <div className={css.askBox}>
                <div className={css.askTitle}>Approve on your device</div>
                <p className={css.muted}>
                  A notification is on its way to your device. If the wallet says there is
                  nothing to approve, link your mailbox first (step 1) and ask again.
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
