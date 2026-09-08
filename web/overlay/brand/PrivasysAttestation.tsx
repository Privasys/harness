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
 * /attest + attestation-server /verify-quote) and shows the verdict as dsh's
 * StateDot plus one word — green ONLY when verified, exactly like every other
 * Privasys property. Clicking opens dsh's Modal with the shared
 * AttestationResultView (the drive security-view pattern) and, beneath it,
 * the policy section: posture AND behaviour, always together.
 *
 * Config arrives from the vanilla auth shell via window.__PRIVASYS_SHELL__:
 * attestUrl (anonymous), verifyQuoteUrl, and getTokenForAudience (the sealed
 * AuthFrame mints attestation-server-audience tokens like chat does).
 */
import { useState } from 'react'
import { Button, Modal, StateDot } from '@deepseek-ai/dsh-client-ui-primitives'
import type { SidebarFooterActionOwnerProps } from '@deepseek-ai/dsh-client-ui-sidebar/client'
import {
  AttestationResultView,
  attestationStatusOf,
  computeAttestationSummary,
  useAttestation,
} from './attestation-view/index.ts'
import { FootRow, ShieldIcon } from './PrivasysFootRow.tsx'
import { PrivasysPolicySection } from './PrivasysPolicy.tsx'
import css from './PrivasysFoot.module.css'

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
// a fresh mint and a fresh verification per render, forever. One module-level
// thunk, one in-flight promise.
let mintPromise: Promise<string> | undefined
function stableAttestationToken(): Promise<string> {
  if (mintPromise === undefined) {
    const mint = shellConfig().getTokenForAudience
    mintPromise = (mint !== undefined ? mint('attestation-server') : Promise.reject(new Error('no token minter')))
      .catch((error: unknown) => {
        mintPromise = undefined
        throw error
      })
  }
  return mintPromise
}

/** dsh StateDot state and the one word the row shows, per verdict. */
const STATUS: Record<string, { dot: 'done' | 'ongoing' | 'error' | 'warning' | 'idle'; word: string }> = {
  verified: { dot: 'done', word: 'Verified' },
  verifying: { dot: 'ongoing', word: 'Checking' },
  failed: { dot: 'error', word: 'Failed' },
  unavailable: { dot: 'warning', word: 'Unavailable' },
}

/**
 * Sidebar-foot row: live attestation state + the full shared report behind it.
 */
export function PrivasysAttestationRow({ wide }: SidebarFooterActionOwnerProps) {
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
  const { status } = attestationStatusOf(summary, Boolean(attestUrl))
  const shown = STATUS[status] ?? { dot: 'idle', word: 'Unknown' }

  return (
    <>
      <FootRow
        wide={wide}
        icon={<ShieldIcon size={wide ? 16 : 18} />}
        label="Attestation"
        status={<><StateDot state={shown.dot} size={10} /><span>{shown.word}</span></>}
        title="Secure Hardware Attestation — verify what you are connected to"
        ariaLabel="Secure Hardware Attestation"
        haspopup="dialog"
        expanded={open}
        onClick={() => { setOpen(true) }}
      />
      <Modal
        open={open}
        onClose={() => { setOpen(false) }}
        title="Secure Hardware Attestation"
        description="Live attestation of the harness enclave that runs your agent. Verify it yourself — you don't have to trust the operator."
        closeLabel="Close"
        className={css.reportDialog}
      >
        <div className={`pv-att-scope ${css.dialogBody}`}>
          {!attestUrl
            ? <p>Attestation is not configured for this instance.</p>
            : state.error && !state.result
              ? (
                <>
                  <p className={css.error}>{state.error}</p>
                  <div className={css.actions}>
                    <Button variant="outline" size="sm" onClick={() => { void actions.inspect() }}>Retry</Button>
                  </div>
                </>
              )
              : !state.result
                ? <p>Verifying the enclave…</p>
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
      </Modal>
    </>
  )
}
