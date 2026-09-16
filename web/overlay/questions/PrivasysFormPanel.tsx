/**
 * Privasys: a question set presented as ONE form.
 *
 * dsh's generic flow shows one question per page with a pager, which suits a
 * conversation ("which of these?") and not a tool that needs three fields at
 * once (an address, a secret, a server). A request whose questions all carry
 * the `form` intent renders here: every field on one card, the tool named
 * in the eyebrow, its message as the title, a short notice under it, a
 * masked input for a secret, and one Submit. The answer encoding is the
 * generic one, so the asker (MCP elicitation, `privasys-elicit.ts`) reads it
 * back exactly as it would the pager's.
 *
 * Kept next to PlanReviewPanel: the composer routes by presentation intent,
 * and this is the third presentation.
 */
import { useMemo, useState, type KeyboardEvent } from 'react'
import clsx from 'clsx'
import { Button, IconCheckOutline14, IconCloseOutline16, MarkdownText } from '@deepseek-ai/dsh-client-ui-primitives'
import type { PendingQuestion, QuestionComposerProps } from './contract/slots.ts'
import { parseRecommendedLabel } from './QuestionComposer.tsx'
import css from './QuestionComposer.module.css'
import form from './PrivasysFormPanel.module.css'

type FormQuestion = PendingQuestion['questions'][number] & {
  secret?: boolean
  required?: boolean
  intent?: { kind: string; title?: string; notice?: string }
}

/** Whether a request is a form: every question carries the intent. */
export function formOf(questions: readonly PendingQuestion['questions'][number][]): boolean {
  return questions.length > 0 && questions.every(q => (q as FormQuestion).intent?.kind === 'form')
}

interface Draft { selected: string[]; custom: string }

const filled = (d: Draft): boolean => d.selected.length > 0 || d.custom.trim() !== ''

export type PrivasysFormPanelProps = { pending: PendingQuestion } & Pick<QuestionComposerProps, 't'>

/**
 * Render every question of the request as one form.
 * @param props - the pending request and the locale seat.
 * @returns The form takeover for this request.
 */
export function PrivasysFormPanel({ pending, t }: PrivasysFormPanelProps) {
  const questions = pending.questions as FormQuestion[]
  const first = questions[0] as FormQuestion
  const markdownLabels = useMemo(() => ({
    code: { copyLabel: t('copy'), copiedLabel: t('copied') },
    footnotes: t('markdown.footnotes'),
  }), [t])
  const [drafts, setDrafts] = useState<Record<string, Draft>>(() =>
    Object.fromEntries(questions.map(q => [q.id, { selected: [], custom: '' }])))
  const [busy, setBusy] = useState<'answer' | 'cancel' | null>(null)
  const [error, setError] = useState<string | null>(null)

  const draftOf = (q: FormQuestion): Draft => drafts[q.id] ?? { selected: [], custom: '' }
  const missing = questions.filter(q => q.required === true && !filled(draftOf(q)))
  const complete = missing.length === 0

  const settle = (kind: 'answer' | 'cancel', send: () => Promise<void>): void => {
    setBusy(kind)
    setError(null)
    void send().catch((cause: unknown) => {
      setBusy(null)
      setError(cause instanceof Error ? cause.message : String(cause))
    })
  }
  const submit = (): void => {
    if (!complete || busy !== null) return
    settle('answer', () => pending.answer({
      answers: questions.map((q) => {
        const d = draftOf(q)
        const custom = d.custom.trim()
        return {
          id: q.id,
          selected: custom === '' || q.multiSelect === true ? d.selected : [],
          ...(custom === '' ? {} : { custom }),
        }
      }),
    }))
  }
  const cancel = (): void => { settle('cancel', () => pending.cancel()) }

  const setCustom = (q: FormQuestion, value: string): void => {
    setDrafts(current => ({ ...current, [q.id]: { selected: q.multiSelect === true ? draftOf(q).selected : [], custom: value } }))
    setError(null)
  }
  const choose = (q: FormQuestion, label: string): void => {
    setDrafts((current) => {
      const d = current[q.id] ?? { selected: [], custom: '' }
      if (q.multiSelect === true) {
        const selected = d.selected.includes(label) ? d.selected.filter(item => item !== label) : [...d.selected, label]
        return { ...current, [q.id]: { ...d, selected } }
      }
      return { ...current, [q.id]: { selected: [label], custom: '' } }
    })
    setError(null)
  }
  const submitOnEnter = (event: KeyboardEvent<HTMLInputElement>): void => {
    if (event.key !== 'Enter' || event.nativeEvent.isComposing) return
    event.preventDefault()
    if (complete) submit()
  }

  const title = first.intent?.title ?? first.question
  const notice = first.intent?.notice

  return (
    <div className={css.frame} data-question-key={pending.key}>
      <section className={css.card} aria-labelledby={`form-${pending.key}`}>
        <header className={css.header}>
          <div className={css.headingBlock}>
            {first.header !== undefined && <div className={css.eyebrow}>{first.header}</div>}
            <h2 className={css.title} id={`form-${pending.key}`}>{title}</h2>
          </div>
          <div className={css.headerActions}>
            <button
              type="button" className={css.iconButton} aria-label={t('nav.cancel')} title={t('nav.cancel')}
              disabled={busy !== null} onClick={cancel}
            >
              <IconCloseOutline16 />
            </button>
          </div>
        </header>

        <div className={css.body} data-question-scroll>
          {notice !== undefined && (
            <div className={form.notice}><MarkdownText text={notice} labels={markdownLabels} /></div>
          )}
          <div className={form.fields}>
            {questions.map((q, i) => {
              const d = draftOf(q)
              const options = q.options ?? []
              return (
                <div className={form.field} key={q.id}>
                  <label className={form.label} htmlFor={`form-${pending.key}-${q.id}`}>
                    {q.question}
                    {q.required === true && <span className={form.required} aria-hidden="true">*</span>}
                  </label>
                  {q.detail !== undefined && <div className={form.hint}>{q.detail}</div>}
                  {options.length > 0
                    ? (
                      <div className={form.choices} role={q.multiSelect === true ? 'group' : 'radiogroup'}>
                        {options.map((option, optionIndex) => {
                          const selected = d.selected.includes(option.label)
                          const display = parseRecommendedLabel(option.label)
                          return (
                            <button
                              type="button" key={`${option.label}-${String(optionIndex)}`}
                              className={clsx(css.option, selected && q.multiSelect !== true && css.optionSelected)}
                              role={q.multiSelect === true ? 'checkbox' : 'radio'}
                              aria-checked={selected}
                              disabled={busy !== null}
                              onClick={() => { choose(q, option.label) }}
                            >
                              {q.multiSelect === true
                                ? (
                                  <span className={clsx(css.checkbox, selected && css.checkboxChecked)} aria-hidden="true">
                                    {selected && <IconCheckOutline14 size={12} />}
                                  </span>
                                )
                                : <span className={css.number}>{optionIndex + 1}</span>}
                              <span className={css.optionCopy}>
                                <span className={css.optionLine}>
                                  <span className={css.optionLabel}>{display.label}</span>
                                  {option.description !== undefined && (
                                    <span className={css.description}>{option.description}</span>
                                  )}
                                </span>
                              </span>
                            </button>
                          )
                        })}
                      </div>
                    )
                    : (
                      <input
                        id={`form-${pending.key}-${q.id}`}
                        className={form.input}
                        type={q.secret === true ? 'password' : 'text'}
                        autoComplete={q.secret === true ? 'new-password' : 'off'}
                        autoFocus={i === 0}
                        value={d.custom}
                        disabled={busy !== null}
                        placeholder={q.secret === true ? '••••••••' : ''}
                        onChange={(event) => { setCustom(q, event.target.value) }}
                        onKeyDown={submitOnEnter}
                      />
                    )}
                </div>
              )
            })}
          </div>
        </div>

        <footer className={css.footer}>
          <div className={css.feedback} role="status">
            {error ?? (complete || missing.length === questions.length ? null : t('error.incomplete'))}
          </div>
          <div className={css.footerActions}>
            <Button variant="outline" disabled={busy !== null} onClick={cancel}>
              {t('nav.cancel')}
            </Button>
            <Button variant="primary" disabled={busy !== null || !complete} onClick={submit}>
              {busy === 'answer' ? t('submitting') : t('submit')}
            </Button>
          </div>
        </footer>
      </section>
    </div>
  )
}
