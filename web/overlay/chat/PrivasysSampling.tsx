/**
 * Sampling pins for the session (attested-harness fork): the composer chip
 * in the `conversation.input.right` seat, beside dsh's model selector.
 *
 * A demo operator pins seed, temperature, top-p, top-k or max tokens for
 * every model call of this session; the measured proxy writes them onto the
 * wire (/privasys/sampling, sampling.go) and Confidential AI echoes what it
 * used in the reproducibility block, so the reply's own panel shows the
 * pins that were honoured, never the ones that were asked for. The chip
 * also tells when a replay is armed for this session and lets it be
 * disarmed. Pins are per session and per user, kept by the proxy for the
 * life of the process.
 */
import { useCallback, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import type { PropsLocale, PropsRuntime } from '@deepseek-ai/dsh-client-ui-slots'
import { Button, Input } from '@deepseek-ai/dsh-client-ui-primitives'
import type {} from '@deepseek-ai/dsh-client-ui-conversation/client'
import { MEASURE_STYLE, useStatDialog } from './stat-dialog.ts'
import { pvGetJson, pvSendJson } from './privasys-fetch.ts'
import type { SamplingPins } from './PrivasysReproducibility.tsx'
import dialogCss from './stat-dialog.module.css'
import css from './PrivasysSampling.module.css'

type Props = PropsRuntime<'conversation.input.right'> & PropsLocale<'chat'>

interface SamplingEntry {
  session: string
  pins?: SamplingPins
  replay?: { of?: { session?: string; turn?: number }; remaining?: number; consumed?: number }
}

type Field = 'seed' | 'temperature' | 'top_p' | 'top_k' | 'max_tokens'
const FIELDS: { key: Field; label: 'input.sampling.seed' | 'input.sampling.temperature' | 'input.sampling.topP' | 'input.sampling.topK' | 'input.sampling.maxTokens'; step: string; integer: boolean }[] = [
  { key: 'seed', label: 'input.sampling.seed', step: '1', integer: true },
  { key: 'temperature', label: 'input.sampling.temperature', step: '0.05', integer: false },
  { key: 'top_p', label: 'input.sampling.topP', step: '0.05', integer: false },
  { key: 'top_k', label: 'input.sampling.topK', step: '1', integer: true },
  { key: 'max_tokens', label: 'input.sampling.maxTokens', step: '1', integer: true },
]

type Draft = Record<Field, string>

function draftOf(pins: SamplingPins | undefined): Draft {
  return {
    seed: pins?.seed === undefined ? '' : String(pins.seed),
    temperature: pins?.temperature === undefined ? '' : String(pins.temperature),
    top_p: pins?.top_p === undefined ? '' : String(pins.top_p),
    top_k: pins?.top_k === undefined ? '' : String(pins.top_k),
    max_tokens: pins?.max_tokens === undefined ? '' : String(pins.max_tokens),
  }
}

function pinsOf(draft: Draft): SamplingPins | null {
  const out: SamplingPins = {}
  for (const f of FIELDS) {
    const raw = draft[f.key].trim()
    if (raw === '') continue
    const n = f.integer ? Number.parseInt(raw, 10) : Number.parseFloat(raw)
    if (!Number.isFinite(n)) continue
    out[f.key] = n
  }
  return Object.keys(out).length === 0 ? null : out
}

function chipLabel(pins: SamplingPins | undefined, fallback: string): string {
  if (pins === undefined) return fallback
  const parts: string[] = []
  if (pins.seed !== undefined) parts.push(`#${pins.seed}`)
  if (pins.temperature !== undefined) parts.push(`T ${pins.temperature}`)
  if (pins.top_p !== undefined) parts.push(`p ${pins.top_p}`)
  if (pins.top_k !== undefined) parts.push(`k ${pins.top_k}`)
  if (pins.max_tokens !== undefined) parts.push(`≤${pins.max_tokens}`)
  return parts.length === 0 ? fallback : parts.join(' · ')
}

/**
 * The sampling chip and its editor.
 * @param props - the current session id and the chat locale seat.
 * @returns the chip; the editor while open.
 */
export function PrivasysSamplingChip({ sessionId, t }: Props) {
  const [entry, setEntry] = useState<SamplingEntry | undefined>(undefined)
  const [draft, setDraft] = useState<Draft>(draftOf(undefined))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)
  const { open, setOpen, rootRef, panelRef, pos } = useStatDialog()
  const path = `/privasys/sampling?session=${encodeURIComponent(sessionId)}`

  const refresh = useCallback(async (): Promise<void> => {
    const e = await pvGetJson<SamplingEntry>(path)
    if (e !== undefined) setEntry(e)
  }, [path])

  useEffect(() => {
    setEntry(undefined)
    void refresh()
  }, [refresh])
  useEffect(() => {
    if (open) {
      void refresh()
      setDraft(draftOf(entry?.pins))
      setError(undefined)
    }
    // The draft seeds from the pins when the editor opens; later refreshes
    // must not overwrite what the user is typing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])
  // An armed replay is consumed by the turn's calls; poll while it is live
  // so the chip clears itself when the replay has run.
  useEffect(() => {
    if (entry?.replay === undefined) return
    const timer = setInterval(() => { void refresh() }, 4000)
    return () => { clearInterval(timer) }
  }, [entry?.replay !== undefined, refresh])

  const send = async (body: { pins?: SamplingPins | null; replay?: null }): Promise<void> => {
    setBusy(true)
    setError(undefined)
    try {
      const e = await pvSendJson<SamplingEntry>('PUT', path, body)
      setEntry(e)
      setDraft(draftOf(e.pins))
    } catch (err: unknown) {
      setError(t('input.sampling.error', { message: err instanceof Error ? err.message : String(err) }))
    } finally {
      setBusy(false)
    }
  }

  const armed = entry?.replay !== undefined
  const pinned = entry?.pins !== undefined
  const label = armed
    ? t('input.sampling.armedShort')
    : chipLabel(entry?.pins, t('input.sampling'))

  return (
    <span ref={rootRef} className={css.root}>
      <button
        type="button"
        className={`${css.chip}${pinned || armed ? ` ${css.active}` : ''}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={t('input.sampling.title')}
        onClick={() => { setOpen(!open) }}
      >
        <DialIcon />
        <span className={css.label}>{label}</span>
      </button>
      {open && createPortal(
        <div
          ref={panelRef}
          className={`${dialogCss.panel} ${css.panel}`}
          role="dialog"
          aria-label={t('input.sampling.title')}
          style={pos ?? MEASURE_STYLE}
        >
          <div className={dialogCss.title}>
            <span className={dialogCss.titleLabel}>
              <DialIcon />
              {t('input.sampling.title')}
            </span>
          </div>
          <div className={dialogCss.titleRule} aria-hidden />
          <p className={css.hint}>{t('input.sampling.hint')}</p>
          {armed && (
            <div className={css.armed}>
              <span>{t('input.sampling.armed', { remaining: String(entry?.replay?.remaining ?? 0) })}</span>
              <Button variant="ghost" size="sm" disabled={busy} onClick={() => { void send({ replay: null }) }}>
                {t('input.sampling.disarm')}
              </Button>
            </div>
          )}
          <div className={css.grid}>
            {FIELDS.map(f => (
              <label key={f.key} className={css.field}>
                <span className={css.fieldLabel}>{t(f.label)}</span>
                <Input
                  type="number"
                  step={f.step}
                  inputMode="decimal"
                  className={css.input ?? ''}
                  value={draft[f.key]}
                  placeholder="—"
                  onChange={(e) => { setDraft({ ...draft, [f.key]: e.target.value }) }}
                />
              </label>
            ))}
          </div>
          <div className={css.footer}>
            <Button variant="primary" size="sm" disabled={busy} onClick={() => { void send({ pins: pinsOf(draft) }) }}>
              {t('input.sampling.apply')}
            </Button>
            <Button variant="ghost" size="sm" disabled={busy || !pinned} onClick={() => { void send({ pins: null }) }}>
              {t('input.sampling.clear')}
            </Button>
          </div>
          {error !== undefined && <div className={css.error}>{error}</div>}
        </div>,
        document.body,
      )}
    </span>
  )
}

/** Dial glyph on the 16-grid at the icon set's stroke weight. */
function DialIcon() {
  return (
    <svg width={14} height={14} viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
      <path d="M2.5 4.5h7M12.5 4.5h1M2.5 11.5h1M6.5 11.5h7" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <circle cx="11" cy="4.5" r="1.6" stroke="currentColor" strokeWidth="1.3" />
      <circle cx="5" cy="11.5" r="1.6" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  )
}
