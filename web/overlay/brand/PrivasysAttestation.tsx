// @ts-nocheck -- interop with the vendored attestation-view: its optional
// props are written `?: T` (websites tsconfig), which dsh's
// exactOptionalPropertyTypes rejects when passing `T | undefined`. The
// vendored files carry their own nocheck for the same reason.
/**
 * "Secure Hardware Attestation" — the harness's attestation surface
 * (attested-harness fork), built on the SHARED @privasys/attestation-view
 * component (vendored as source under ./attestation-view; canonical copy in
 * websites/libs/attestation-view) so this view can never drift from
 * chat/drive/store/developer-portal.
 *
 * The sidebar-foot row runs the live attestation on mount (management-service
 * /attest + attestation-server /verify-quote) and renders the shared
 * AttestationStatusBadge — green ONLY when verified, exactly like every other
 * Privasys property. Clicking opens a full-screen panel with the shared
 * AttestationResultView (the drive security-view pattern).
 *
 * Config arrives from the vanilla auth shell via window.__PRIVASYS_SHELL__:
 * attestUrl (anonymous), verifyQuoteUrl, and getTokenForAudience (the sealed
 * AuthFrame mints attestation-server-audience tokens like chat does).
 */
import { useEffect, useState } from 'react'
import type { SidebarFooterActionOwnerProps } from '@deepseek-ai/dsh-client-ui-sidebar/client'
import {
  AttestationResultView,
  AttestationStatusBadge,
  attestationStatusOf,
  computeAttestationSummary,
  useAttestation,
} from './attestation-view/index.ts'
import { ensureRowStyles } from './PrivasysRows.tsx'
import { PrivasysPolicySection } from './PrivasysPolicy.tsx'

interface ShellAttestationConfig {
  attestUrl?: string
  verifyQuoteUrl?: string
  getTokenForAudience?: (audience: string) => Promise<string>
}

function shellConfig(): ShellAttestationConfig {
  return (globalThis as { __PRIVASYS_SHELL__?: ShellAttestationConfig }).__PRIVASYS_SHELL__ ?? {}
}

// SINGLE-FLIGHT audience-token mint with a STABLE function identity.
// useAttestation's auto-verify effect depends on the token thunk; an inline
// arrow (new identity per render) re-fired the effect on every state change —
// with a failing mint that meant render -> mint 400 -> setState -> render...
// an infinite loop that hammered the IdP (hundreds of POST /token 400) and
// starved the page. One mint per page load; failure memoized (no retries —
// quote verification then reports one error and stops).
let mintPromise: Promise<string> | null = null
function stableAttestationToken(): Promise<string> {
  if (mintPromise === null) {
    const cfg = shellConfig()
    mintPromise = typeof cfg.getTokenForAudience === 'function'
      ? cfg.getTokenForAudience('attestation-server')
      : Promise.reject(new Error('auth frame not ready'))
    // Memoize rejection too; surface it, never loop on it.
    mintPromise.catch(() => {})
  }
  return mintPromise
}

const ROW_STYLE_ID = 'privasys-attestation-styles'
const ROW_STYLE = `
.pv-att-overlay { position: fixed; inset: 0; z-index: 2147482500;
  background: rgba(0, 0, 0, 0.5); display: flex; align-items: stretch; justify-content: center; }
.pv-att-panel { margin: 24px; flex: 1; max-width: 860px; overflow-y: auto;
  border-radius: 14px; background: Canvas; color: CanvasText; padding: 24px 28px;
  box-shadow: 0 18px 60px rgba(0, 0, 0, 0.4); }
.pv-att-head { display: flex; align-items: center; justify-content: space-between; gap: 12px;
  margin-bottom: 4px; }
.pv-att-title { font-size: 20px; font-weight: 600; margin: 0; }
.pv-att-sub { font-size: 13px; opacity: 0.7; margin: 4px 0 18px; }
.pv-att-close { border: none; background: transparent; color: inherit; font-size: 22px;
  cursor: pointer; opacity: 0.7; padding: 4px 8px; border-radius: 8px; }
.pv-att-close:hover { opacity: 1; background: rgba(128, 128, 128, 0.15); }
`

function ensureStyles(): void {
  if (document.getElementById(ROW_STYLE_ID) !== null) return
  const tag = document.createElement('style')
  tag.id = ROW_STYLE_ID
  tag.textContent = ROW_STYLE
  document.head.appendChild(tag)
}

function ShieldIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
      <path d="m9 12 2 2 4-4" />
    </svg>
  )
}

const STATUS_COLOR: Record<string, string> = {
  verified: '#059669', // emerald-600 — green means VERIFIED, never decoration
  verifying: 'inherit',
  failed: '#dc2626',
  unavailable: '#d97706',
}

/**
 * Sidebar-foot row: live attestation state + the full shared report behind it.
 */
export function PrivasysAttestationRow({ wide }: SidebarFooterActionOwnerProps) {
  useEffect(() => { ensureRowStyles(); ensureStyles() }, [])
  const [open, setOpen] = useState(false)
  const cfg = shellConfig()
  const attestUrl = cfg.attestUrl ?? ''
  const verifyQuoteUrl = cfg.verifyQuoteUrl ?? 'https://as.privasys.org/verify-quote'
  // The management-service /attest report is ANONYMOUS — never gate it on a
  // token (a mint failure must not blank the whole report). Only the
  // attestation-server quote verification needs the audience token, minted
  // once via the module-level stable thunk (see stableAttestationToken).
  const [state, actions] = useAttestation({
    attestUrl,
    verifyQuoteUrl,
    verifyQuoteToken: stableAttestationToken,
    autoInspect: Boolean(attestUrl),
    autoVerifyQuote: Boolean(attestUrl),
  })
  const summary = computeAttestationSummary(state, undefined)
  const { status, reason } = attestationStatusOf(summary, Boolean(attestUrl))

  useEffect(() => {
    if (!open) return
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('keydown', onKey)
    return () => { document.removeEventListener('keydown', onKey) }
  }, [open])

  return (
    <>
      <button
        type="button"
        className={`pv-row${wide ? '' : ' pv-row-narrow'}`}
        style={{ color: STATUS_COLOR[status] ?? 'inherit' }}
        title="Secure Hardware Attestation — verify what you are connected to"
        aria-label="Secure Hardware Attestation"
        onClick={() => { setOpen(true) }}
      >
        <ShieldIcon size={wide ? 16 : 18} />
        {wide
          ? (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                Secure Hardware Attestation
              </span>
              {/* Tick only — the full "Verified" label does not fit the row. */}
              <AttestationStatusBadge status={status} reason={reason} verifiedLabel="" />
            </span>
          )
          : null}
      </button>
      {open
        ? (
          <div className="pv-att-overlay" onClick={() => { setOpen(false) }}>
            <div
              className="pv-att-panel pv-att-scope"
              role="dialog"
              aria-label="Secure Hardware Attestation"
              onClick={(event) => { event.stopPropagation() }}
            >
              <div className="pv-att-head">
                <h1 className="pv-att-title">Secure Hardware Attestation</h1>
                <button type="button" className="pv-att-close" aria-label="Close"
                  onClick={() => { setOpen(false) }}>×</button>
              </div>
              <p className="pv-att-sub">
                Live attestation of the harness enclave that runs your agent.
                Verify it yourself — you don&apos;t have to trust the operator.
              </p>
              {!attestUrl
                ? <div>Attestation is not configured for this instance.</div>
                : state.error && !state.result
                  ? (
                    <div>
                      <div style={{ color: '#dc2626', marginBottom: 12 }}>{state.error}</div>
                      <button type="button" className="pv-row" style={{ width: 'auto' }}
                        onClick={() => { void actions.inspect() }}>Retry</button>
                    </div>
                  )
                  : !state.result
                    ? <div>Verifying the enclave…</div>
                    : (
                      <AttestationResultView
                        result={state.result}
                        quoteVerify={state.quoteVerify}
                        quoteVerifying={state.verifying}
                        quoteVerifyError={state.quoteVerifyError}
                        challenge={state.challenge}
                        onRegenerateChallenge={actions.regenerateChallenge}
                        onRefresh={() => { void actions.inspect() }}
                        loading={state.loading}
                        verifyQuoteUrl={verifyQuoteUrl}
                      />
                    )}
              {/* Posture AND behaviour, always together. */}
              <PrivasysPolicySection />
            </div>
          </div>
        )
        : null}
    </>
  )
}
