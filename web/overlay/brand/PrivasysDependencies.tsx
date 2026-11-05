// @ts-nocheck -- same vendored-interop reason as PrivasysAttestation.tsx.
/**
 * The dependencies section of the attestation panel: every enclave this
 * harness will talk to, each with its own live attestation.
 *
 * The report above this section proves the harness. What the agent does with
 * your data also runs in the enclaves it depends on (the model, the tools,
 * Drive), so a verdict on the harness alone says nothing about them.
 *
 * The list is the harness's PINNED set, read from the measured proxy
 * (GET /privasys/attestation), the same set its mutual RA-TLS refuses to
 * deviate from. Each row then attests that app live, through the shared
 * composite view: the control plane's anonymous /attest relays the evidence,
 * and the attestation server verifies the quote against a challenge chosen in
 * this browser. The pinned app id and code hash go in as expectations, so a
 * row turns green only when the live enclave is the one the harness pins.
 */
import { useEffect, useMemo, useState } from 'react'
import { CompositeAttestationView } from './attestation-view/index.ts'

/** The control plane that answers for a host: dev apps live on test. */
function controlPlaneFor(host) {
  return /\.apps\.test\.privasys\.org$/i.test(host ?? '')
    ? 'https://api.developer.test.privasys.org'
    : 'https://api.developer.privasys.org'
}

function dashed(hex) {
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

/** Dependency pins arrive base64; the attestation view compares OIDs in hex. */
function base64ToHex(value) {
  try {
    const raw = atob(value)
    let hex = ''
    for (let index = 0; index < raw.length; index += 1) {
      hex += raw.charCodeAt(index).toString(16).padStart(2, '0')
    }
    return hex
  } catch {
    return value
  }
}

/**
 * One row per pinned app. The proxy learns which host is which app from the
 * verified connection, so a dependency not yet dialled since start has no name
 * or host: it is still listed and attested, by its id, on this harness's own
 * control plane.
 */
function targetsOf(doc, verifyQuoteUrl, ownAttestBase) {
  const tools = doc?.tools ?? []
  return (doc?.dependencies ?? []).map((dep) => {
    const tool = tools.find(entry => entry.app_id === dep.app_id)
    const isModel = doc?.model_app_id === dep.app_id
    const host = isModel ? doc.model_host : tool?.host
    const label = isModel ? 'Confidential AI (the model)' : (tool?.label || `Attested app ${dep.app_id.slice(0, 8)}`)
    const base = host !== undefined && host !== '' ? controlPlaneFor(host) : ownAttestBase
    return {
      id: dep.app_id,
      label,
      ...(host ? { description: host } : {}),
      attestUrl: `${base}/api/v1/apps/${dashed(dep.app_id)}/attest`,
      verifyQuoteUrl,
      expectations: {
        appId: dep.app_id,
        ...(dep.code_hash !== undefined ? { workloadImageDigest: base64ToHex(dep.code_hash) } : {}),
        labels: {
          appId: 'pinned in the harness dependency set',
          workloadImageDigest: 'pinned in the harness dependency set',
        },
      },
    }
  })
}

/**
 * @param props - the attestation-server token thunk, attest base and verify URL.
 * @returns the section; it attests only while the panel is open.
 */
export function PrivasysDependenciesSection({ verifyQuoteAuth, verifyQuoteUrl, attestBase }) {
  const [doc, setDoc] = useState(undefined)
  useEffect(() => {
    let current = true
    void fetch('/privasys/attestation')
      .then(async response => (response.ok ? await response.json() : null))
      .then((value) => { if (current) setDoc(value) }, () => { if (current) setDoc(null) })
    return () => { current = false }
  }, [])
  const targets = useMemo(
    () => targetsOf(doc, verifyQuoteUrl, attestBase),
    [doc, verifyQuoteUrl, attestBase],
  )

  return (
    <section style={{ marginTop: 20 }}>
      <h3 style={{ fontSize: 14, fontWeight: 600, margin: '0 0 4px' }}>What this harness depends on</h3>
      <p style={{ fontSize: 13, color: '#6b7280', margin: '0 0 12px' }}>
        Your agent also runs on these enclaves. The harness refuses any connection to a peer that does
        not match its pinned identity. Each one is attested here, live and separately.
      </p>
      {doc === undefined && <p style={{ fontSize: 13 }}>Reading the dependency set…</p>}
      {doc === null && <p style={{ fontSize: 13 }}>Could not read the dependency set from the harness.</p>}
      {doc && targets.length === 0 && <p style={{ fontSize: 13 }}>This harness pins no dependencies.</p>}
      {targets.length > 0 && (
        <CompositeAttestationView targets={targets} autoInspect verifyQuoteAuth={verifyQuoteAuth} />
      )}
    </section>
  )
}
