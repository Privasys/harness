/**
 * "What this deployment runs" (attested-harness fork): the composition that
 * boots, as the build dumped and asserted it and the proxy serves it
 * (composition.go). Every row, on or off, and what each agent mode composes
 * under it; then the rows the proxy adds for every user at start.
 *
 * In the Attestation panel, not in Settings: these are not settings (nothing
 * here can be changed from the browser), they are what the measured image
 * runs, and this is where the evidence that it is measured sits. A person
 * who asks "what exactly runs here?" reads the answer beside the proof.
 */
import { useEffect, useState } from 'react'
import css from './PrivasysFoot.module.css'

interface Row {
  id: string
  name?: string
  disabled?: boolean
  rows?: Row[]
}

interface Composition {
  image?: string
  rows: Row[]
  per_user: Row[]
}

function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

function RowLine({ row }: { row: Row }) {
  return (
    <li className={css.compRow}>
      <span className={row.disabled ? css.compOff : undefined}>{row.id}</span>
      {row.name !== undefined && row.name !== row.id && <code className={css.compName}>{row.name}</code>}
      {row.disabled && <span className={css.compBadge}>off</span>}
    </li>
  )
}

function RowList({ rows }: { rows: Row[] }) {
  return <ul className={css.compList}>{rows.map(r => <RowLine key={r.id} row={r} />)}</ul>
}

export function PrivasysCompositionSection() {
  const [state, setState] = useState<Composition | { error: string } | undefined>(undefined)
  useEffect(() => {
    let stopped = false
    void pvFetch('/privasys/composition')
      .then(async r => {
        const d = await r.json() as Composition & { error?: string }
        if (stopped) return
        setState(r.ok ? d : { error: d.error ?? 'The composition could not be read.' })
      }, () => { if (!stopped) setState({ error: 'The composition could not be read.' }) })
    return () => { stopped = true }
  }, [])

  const presets = state !== undefined && 'rows' in state ? state.rows.filter(r => (r.rows?.length ?? 0) > 0) : []
  const plain = state !== undefined && 'rows' in state ? state.rows.filter(r => (r.rows?.length ?? 0) === 0) : []

  return (
    <section className={css.compSection}>
      <h3 className={css.compTitle}>What this deployment runs</h3>
      <p className={css.muted}>
        The parts this image composes at boot, as the build asserted them: every row,
        on or off, and what each agent mode adds. Nothing here is a setting; it is
        what the measured image runs, and the measurement above covers it.
      </p>
      {state === undefined && <p className={css.muted}>Reading the composition…</p>}
      {state !== undefined && 'error' in state && <p className={css.warn}>{state.error}</p>}
      {state !== undefined && 'rows' in state && (
        <>
          <details className={css.compGroup}>
            <summary className={css.compSummary}>Always ({plain.length} rows)</summary>
            <RowList rows={plain} />
          </details>
          {presets.map(p => (
            <details key={p.id} className={css.compGroup}>
              <summary className={css.compSummary}>
                {p.id}{p.disabled ? ' (off)' : ''} ({p.rows?.length ?? 0} rows)
              </summary>
              <RowList rows={p.rows ?? []} />
            </details>
          ))}
          <details className={css.compGroup}>
            <summary className={css.compSummary}>Added for each user at start ({state.per_user.length} rows)</summary>
            <RowList rows={state.per_user} />
          </details>
          {state.image !== undefined && state.image !== '' && (
            <p className={css.muted}>Image <code>{state.image.slice(0, 16)}…</code></p>
          )}
        </>
      )}
    </section>
  )
}
