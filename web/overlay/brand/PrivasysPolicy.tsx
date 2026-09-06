// @ts-nocheck -- same vendored-interop reason as PrivasysAttestation.tsx.
/**
 * The policy section of the attestation panel: what this harness PERMITS, and
 * what it has actually REACHED.
 *
 * Two lines, never one. A harness's posture and its behaviour are different
 * claims. A permissive posture whose session only ever spoke to attested
 * enclaves deserves to read as attested; a strict-looking harness that quietly
 * fetched from twenty sites must not hide it behind its own settings. Showing
 * either half alone is how an honest product acquires a false badge.
 *
 * Both come from the measured Go proxy, same-origin:
 *   GET /privasys/policy      the ceiling, its exact text, and its digest
 *   GET /privasys/egress-log  what was actually reached (needs a session)
 *
 * The digest is the load-bearing part. It is published in this enclave's
 * serving certificate at OID 1.3.6.1.4.1.65230.5.4.9, so a reader does not
 * have to trust this panel: fetch the ceiling text, hash it, and compare with
 * the leaf. That is the whole reason the policy is attested by digest rather
 * than by a per-user certificate.
 */
import { useEffect, useState } from 'react'

const MODE_PROSE: Record<string, string> = {
  none: 'No network access at all. Local tools only.',
  tee_only: 'Only attested enclaves. No direct fetch of any kind.',
  allowlist: 'Attested enclaves, plus the named hosts below.',
  proxy: 'All traffic is forwarded to your own firewall.',
  open: 'Any host may be reached, through the measured proxy, and every connection is recorded.',
}

const ASSURANCE_PROSE: Record<string, string> = {
  attested: 'attested — mutual RA-TLS to a peer whose measurement matched',
  'confidential-transport': 'encrypted to a named host, but that host is not attested',
  open: 'encrypted, but the host is neither named nor attested',
}

const ASSURANCE_COLOR: Record<string, string> = {
  attested: '#059669',
  'confidential-transport': '#d97706',
  open: '#6b7280',
}

function Field({ label, children }: { label: string, children: unknown }) {
  return (
    <div style={{ display: 'flex', gap: 10, padding: '3px 0', fontSize: 13 }}>
      <span style={{ minWidth: 132, color: '#6b7280', flexShrink: 0 }}>{label}</span>
      <span style={{ minWidth: 0, wordBreak: 'break-word' }}>{children}</span>
    </div>
  )
}

export function PrivasysPolicySection() {
  const [policy, setPolicy] = useState(undefined)
  const [log, setLog] = useState(undefined)

  useEffect(() => {
    let live = true
    void fetch('/privasys/policy')
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => { if (live) setPolicy(d ?? null) }, () => { if (live) setPolicy(null) })
    // The egress record needs a signed-in session; a 401 here is normal and
    // simply means this reader is anonymous, not that anything is wrong.
    void fetch('/privasys/egress-log')
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => { if (live) setLog(d ?? null) }, () => { if (live) setLog(null) })
    return () => { live = false }
  }, [])

  if (policy === undefined) return <div style={{ fontSize: 13 }}>Reading the policy…</div>
  if (policy === null) {
    return (
      <div style={{ fontSize: 13, color: '#6b7280' }}>
        This harness does not publish a policy document.
      </div>
    )
  }

  const ceiling = policy.summary?.ceiling
  const tenant = policy.summary?.tenant
  const effective = policy.summary?.effective
  const hosts = log?.hosts_reached ? Object.keys(log.hosts_reached) : []
  const floor = log?.assurance_floor ?? ''

  return (
    <div style={{ marginTop: 18 }}>
      <h2 style={{ fontSize: 15, fontWeight: 600, margin: '0 0 2px' }}>Policy</h2>
      <p style={{ fontSize: 12.5, color: '#6b7280', margin: '0 0 10px' }}>
        What this harness is permitted to do, and what it has actually done. These
        are different claims and both are shown.
      </p>

      <div style={{ border: '1px solid #e5e7eb', borderRadius: 8, padding: '10px 12px', marginBottom: 10 }}>
        <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 4 }}>Permitted</div>
        <Field label="Posture">
          <strong>{effective?.mode ?? ceiling?.mode}</strong>
          <span style={{ color: '#6b7280' }}>
            {' — '}{MODE_PROSE[effective?.mode ?? ceiling?.mode] ?? ''}
          </span>
        </Field>
        {ceiling?.allowlist?.length
          ? <Field label="Named hosts">{ceiling.allowlist.join(', ')}</Field>
          : null}
        {tenant
          ? (
            <Field label="Your own policy">
              <strong>{tenant.mode}</strong>
              <span style={{ color: '#6b7280' }}>
                {' — you have narrowed this harness further; it can never widen what the operator permits'}
              </span>
            </Field>
          )
          : null}
        <Field label="Policy digest">
          <code style={{ fontSize: 11.5 }}>{ceiling?.digest ?? '—'}</code>
        </Field>
        <p style={{ fontSize: 12, color: '#6b7280', margin: '6px 0 0' }}>
          Published in this enclave&apos;s certificate at{' '}
          <code style={{ fontSize: 11.5 }}>{policy.policy_digest_oid}</code>. You do not
          have to trust this panel: fetch <code style={{ fontSize: 11.5 }}>/privasys/policy</code>,
          hash the document, and compare.
        </p>
      </div>

      <div style={{ border: '1px solid #e5e7eb', borderRadius: 8, padding: '10px 12px' }}>
        <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 4 }}>Actually reached</div>
        {log === undefined
          ? <div style={{ fontSize: 13 }}>Reading the record…</div>
          : log === null
            ? (
              <div style={{ fontSize: 12.5, color: '#6b7280' }}>
                Sign in to see what this harness has reached. The record is kept per
                session and is not published anonymously.
              </div>
            )
            : (
              <>
                <Field label="Direct fetches">
                  {log.allowed_total === 0 && log.refused_total === 0
                    ? 'None. Everything this agent did went through attested enclaves.'
                    : `${log.allowed_total} allowed, ${log.refused_total} refused by policy`}
                </Field>
                {hosts.length
                  ? <Field label="Hosts reached">{hosts.join(', ')}</Field>
                  : null}
                {floor
                  ? (
                    <Field label="Weakest step">
                      <span style={{ color: ASSURANCE_COLOR[floor] ?? 'inherit' }}>
                        {ASSURANCE_PROSE[floor] ?? floor}
                      </span>
                    </Field>
                  )
                  : null}
                {log.allowed_total > 0
                  ? (
                    <p style={{ fontSize: 12, color: '#6b7280', margin: '6px 0 0' }}>
                      A direct fetch is never attested. Work that used only the attested
                      tools is unaffected — assurance is recorded per step, so one
                      direct fetch does not downgrade the rest.
                    </p>
                  )
                  : null}
              </>
            )}
      </div>
    </div>
  )
}
