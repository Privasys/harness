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

// The sealed transport the shell installs carries the relay-asserted subject;
// the per-user reads (your own policy, the record, your spending) and the
// policy write need it. Off-platform the raw fetch is right.
function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

/**
 * Standing consent to per-call tool fees. A priced attested tool answers
 * with its exact price; the proxy consents on the user's behalf ONLY within
 * the figures set here, which live in the user's own policy document in
 * their Drive. The model is never asked: it would approve anything.
 */
function SpendingSection({ policy, onSaved }: { policy: any; onSaved: () => void }) {
  const [spend, setSpend] = useState<any>(undefined)
  const [perCall, setPerCall] = useState('')
  const [perSession, setPerSession] = useState('')
  const [saving, setSaving] = useState(false)
  const [notice, setNotice] = useState('')

  const load = (): void => {
    void pvFetch('/privasys/spend')
      .then(async r => (r.ok ? await r.json() : null))
      .then(d => {
        setSpend(d ?? null)
        if (d?.spend) {
          setPerCall(String(d.spend.per_call_max_credits ?? ''))
          setPerSession(String(d.spend.per_session_max_credits ?? ''))
        }
      }, () => { setSpend(null) })
  }
  useEffect(load, [])

  const save = (): void => {
    const pc = Number(perCall || 0)
    const ps = Number(perSession || 0)
    if (!Number.isFinite(pc) || pc < 0 || !Number.isFinite(ps) || ps < 0) {
      setNotice('Enter whole numbers of credits.')
      return
    }
    // Start from the user's existing document so nothing else they set is
    // lost; otherwise a minimal one at the harness's own posture (a tenant
    // document must state a mode and may only narrow — the same mode is the
    // identity narrowing).
    let doc: any
    try { doc = policy?.tenant_document ? JSON.parse(JSON.stringify(policy.tenant_document)) : undefined } catch { doc = undefined }
    if (!doc) {
      doc = { policy: 'privasys.harness/v1', scope: 'tenant', egress: { mode: policy?.summary?.effective?.mode ?? policy?.summary?.ceiling?.mode ?? 'none' } }
    }
    const spendDoc: any = {}
    if (pc > 0) spendDoc.per_call_max_credits = pc
    if (ps > 0) spendDoc.per_session_max_credits = ps
    if (doc.spend?.tools) spendDoc.tools = doc.spend.tools
    if (Object.keys(spendDoc).length) doc.spend = spendDoc
    else delete doc.spend
    setSaving(true)
    setNotice('')
    void pvFetch('/privasys/policy/tenant', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(doc, null, 2),
    })
      .then(async r => {
        const body = await r.json().catch(() => ({}))
        if (!r.ok) { setNotice(body.error ?? `HTTP ${r.status}`); return }
        setNotice(body.persisted === false
          ? 'Applied for this session. Connect your Drive to keep it.'
          : 'Saved to your Drive.')
        load(); onSaved()
      }, e => { setNotice(String(e)) })
      .finally(() => { setSaving(false) })
  }

  if (spend === undefined) return null
  const total = spend?.session_total_credits ?? 0
  const charges = spend?.charges ?? []
  return (
    <div style={{ border: '1px solid #e5e7eb', borderRadius: 8, padding: '10px 12px', marginBottom: 10 }}>
      <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 4 }}>Spending on paid tools</div>
      <p style={{ fontSize: 12, color: '#6b7280', margin: '0 0 8px' }}>
        Some attested tools charge per call, and the charge is yours. This harness
        consents on your behalf only within the limits you set here; a call above
        them is refused and the agent is told to ask you. Limits are kept in your
        Drive with the rest of your policy.
      </p>
      {spend === null
        ? <div style={{ fontSize: 12.5, color: '#6b7280' }}>Sign in to set spending limits.</div>
        : (
          <>
            <Field label="Per call, at most">
              <input value={perCall} onChange={e => { setPerCall(e.target.value) }} inputMode="numeric"
                placeholder="0 = nothing approved" style={{ width: 160, fontSize: 12.5 }} />{' '}credits
            </Field>
            <Field label="Per session, at most">
              <input value={perSession} onChange={e => { setPerSession(e.target.value) }} inputMode="numeric"
                placeholder="empty = no session cap" style={{ width: 160, fontSize: 12.5 }} />{' '}credits
            </Field>
            {spend?.ceiling_spend
              ? (
                <Field label="Operator cap">
                  {spend.ceiling_spend.per_call_max_credits ? `${spend.ceiling_spend.per_call_max_credits} per call` : ''}
                  {spend.ceiling_spend.per_session_max_credits ? ` · ${spend.ceiling_spend.per_session_max_credits} per session` : ''}
                </Field>
              )
              : null}
            <Field label="Charged this session">
              {total} credits{charges.length ? ` across ${charges.length} call(s)` : ''}
            </Field>
            <button type="button" className="pv-row" style={{ width: 'auto', marginTop: 6 }} disabled={saving} onClick={save}>
              {saving ? 'Saving…' : 'Save limits'}
            </button>
            {notice ? <div style={{ fontSize: 12, color: '#6b7280', marginTop: 6 }}>{notice}</div> : null}
          </>
        )}
    </div>
  )
}

export function PrivasysPolicySection() {
  const [policy, setPolicy] = useState(undefined)
  const [log, setLog] = useState(undefined)
  const [tick, setTick] = useState(0)

  useEffect(() => {
    let live = true
    void pvFetch('/privasys/policy')
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => { if (live) setPolicy(d ?? null) }, () => { if (live) setPolicy(null) })
    // The egress record needs a signed-in session; a 401 here is normal and
    // simply means this reader is anonymous, not that anything is wrong.
    void pvFetch('/privasys/egress-log')
      .then(async r => (r.ok ? await r.json() : undefined))
      .then(d => { if (live) setLog(d ?? null) }, () => { if (live) setLog(null) })
    return () => { live = false }
  }, [tick])

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

      <SpendingSection policy={policy} onSaved={() => { setTick(t => t + 1) }} />

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
