/**
 * Reproducibility per reply (attested-harness fork).
 *
 * Confidential AI ends every stream with a reproducibility block — the seed
 * it drew, the sampling it used, the model, weights, vLLM/CUDA/GPU, the TEE,
 * the clock it injected. The measured proxy opts every model call into it,
 * annotates it (prompt digest, pins applied, replay verdict) and the
 * translator folds it into the assistant message's `usage`
 * (apply-overlay.mjs §2h), so it lives in the session log beside the tokens.
 *
 * This is the `conversation.chat.assistant-actions` entry that shows it: a
 * pill in the turn tail's IconActions row (the Turn-usage pill's own skin,
 * TurnUsagePanel.module.css) opening the shared stat-dialog with the facts,
 * and "Replay this turn": fork the session before this turn, arm the proxy
 * with every recorded call of the turn, and send the prompt again. Same
 * model, weights, seed and prompt on the same hardware reproduce the reply
 * byte for byte — and the panel says whether the prompt matched and whether
 * the reply did, rather than assuming either.
 */
import { useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import type { Context } from '@deepseek-ai/cordis'
import type { SessionId } from '@deepseek-ai/dsh-session/types'
import type { InjectFace, PropsLocale, PropsRuntime } from '@deepseek-ai/dsh-client-ui-slots'
import { Button, writeClipboard } from '@deepseek-ai/dsh-client-ui-primitives'
import type {} from '@deepseek-ai/dsh-api-session-controller/client'
import type {} from '@deepseek-ai/dsh-api-workspace-controller/client'
import type {} from '@deepseek-ai/dsh-client-ui-conversation/client'
import type { AssistantMessageNode, ConversationNode, ToolResultNode } from '../contract/snapshot.ts'
import type { ChatViewSlotProps } from '../contract/slots.ts'
import { MEASURE_STYLE, useStatDialog } from './stat-dialog.ts'
import { pvSendJson } from './privasys-fetch.ts'
import pillCss from './TurnUsagePanel.module.css'
import dialogCss from './stat-dialog.module.css'
import css from './PrivasysReproducibility.module.css'

/** Sampling fields, as Confidential AI echoes them and the proxy pins them. */
export interface SamplingPins {
  seed?: number
  temperature?: number
  top_p?: number
  top_k?: number
  max_tokens?: number
}

/** The proxy's annotation on the block (sampling.go, modelCall.annotation). */
export interface HarnessAnnotation {
  request_id?: string
  session?: string
  at?: string
  prompt_digest?: string
  time_context?: string
  messages_count?: number
  tools_count?: number
  pins?: SamplingPins
  replay?: {
    of?: { session?: string; message_id?: string; turn?: number }
    step?: number
    skipped?: string
    prompt_match?: boolean
    expected_prompt_digest?: string
    /** Whether the model stamped the recorded clock, not a fresh one (repro.go). */
    dynamic_context_match?: boolean
    /** Recorded tool results served so far in this replay (sampling.go). */
    tools_replayed?: number
  }
}

/** Confidential AI's block (internal/reproducibility/metadata.go) plus ours. */
export interface ReproBlock extends SamplingPins {
  request_id?: string
  model?: string
  quantization?: string
  vllm_version?: string
  cuda_version?: string
  gpu?: string
  tensor_parallel_size?: number
  batch_invariance?: boolean
  image_digest?: string
  tee_type?: string
  timestamp?: string
  dynamic_context?: string
  kv_cache_mode?: string
  harness?: HarnessAnnotation
}

/** What the action needs from apply.ts: the replay choreography. */
export interface PrivasysReproInjected {
  replay: (request: ReplayRequest) => Promise<void>
}

/** One recorded model call of the turn, as the proxy expects it back. */
export interface ReplayStep extends SamplingPins {
  dynamic_context?: string
  time_context?: string
  prompt_digest?: string
  messages_count: number
  tools_count: number
}

/**
 * One recorded tool call of the turn, as the proxy expects it back: the
 * attested tool app (`mcp__<server>__<name>` on the model's side), the
 * canonical digest of the arguments the model gave it, and the result it
 * produced. The proxy serves this result to an identical call during the
 * replay instead of dialling the tool, so the next model call sees the same
 * prompt it saw the first time.
 */
export interface ReplayTool {
  server: string
  name: string
  args_digest: string
  content: { type: string; text?: string }[]
  is_error?: boolean
}

export interface ReplayRequest {
  messageId: string
  turn: number
  /** Seq of the previous turn's end; absent for turn 1 (a new session is used). */
  prevTurnEndSeq: number | undefined
  prompt: string
  steps: ReplayStep[]
  /** The turn's attested tool results, in call order. */
  tools: ReplayTool[]
  /** The final reply's text, hashed and kept locally to judge the replay. */
  replyText: string
}

/**
 * Canonical JSON for a tool call's arguments, the way the proxy digests
 * them (sampling.go argsDigest: keys sorted, no escaping beyond JSON's own,
 * no whitespace), so a replayed call and its record meet on content.
 */
function canonicalJson(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(canonicalJson).join(',')}]`
  if (v !== null && typeof v === 'object') {
    const o = v as Record<string, unknown>
    return `{${Object.keys(o).sort().map(k => `${JSON.stringify(k)}:${canonicalJson(o[k])}`).join(',')}}`
  }
  return JSON.stringify(v)
}

/** The turn's attested tool results as replay records; local tools are not proxied and stay live. */
async function replayToolsOf(results: readonly ToolResultNode[]): Promise<ReplayTool[]> {
  const out: ReplayTool[] = []
  for (const r of results) {
    if (r.call === null) continue
    const m = /^mcp__([A-Za-z0-9_-]+?)__(.+)$/.exec(r.call.name)
    if (m === null) continue
    let args: unknown
    try {
      args = JSON.parse(r.call.argsRaw)
    } catch {
      args = r.call.argsRaw
    }
    out.push({
      server: m[1] as string,
      name: m[2] as string,
      args_digest: await sha256Hex(canonicalJson(args)),
      content: r.content
        .filter(b => (b as { type?: string }).type === 'text')
        .map(b => ({ type: 'text', text: (b as { text: string }).text })),
      ...(r.isError ? { is_error: true } : {}),
    })
  }
  return out
}

type Props = PropsRuntime<'conversation.chat.assistant-actions'>
  & InjectFace<PrivasysReproInjected>
  & PropsLocale<'chat'>

type T = ChatViewSlotProps['t']

const REPLAY_STORE_PREFIX = 'privasys-replay:'

interface StoredReplay {
  of: { session: string; message_id: string; turn: number }
  reply_sha256: string
}

function reproOf(usage: unknown): ReproBlock | undefined {
  if (typeof usage !== 'object' || usage === null) return undefined
  const block = (usage as { reproducibility?: unknown }).reproducibility
  return typeof block === 'object' && block !== null ? block as ReproBlock : undefined
}

function assistantText(node: AssistantMessageNode): string {
  return node.blocks.filter(b => b.kind === 'text').map(b => (b as { text: string }).text).join('')
}

function userText(node: ConversationNode & { kind: 'user' }): string {
  return node.content
    .filter(part => (part as { type?: string }).type === 'text')
    .map(part => (part as { text: string }).text)
    .join('\n')
}

async function sha256Hex(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  return Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, '0')).join('')
}

function readStoredReplay(sessionId: string): StoredReplay | undefined {
  try {
    const raw = localStorage.getItem(REPLAY_STORE_PREFIX + sessionId)
    return raw === null ? undefined : JSON.parse(raw) as StoredReplay
  } catch {
    return undefined
  }
}

/** The turn's assistant steps, its tool results, the prompt that opened it, and the fork anchor. */
interface Located {
  node: AssistantMessageNode
  steps: AssistantMessageNode[]
  /** The turn's tool results, in call order (logged between its steps). */
  toolResults: ToolResultNode[]
  prompt: string | undefined
  prevTurnMaxSeq: number | undefined
}

function locate(nodes: readonly ConversationNode[], messageId: string): Located | undefined {
  const idx = nodes.findIndex(n => n.kind === 'assistant' && n.messageId === messageId)
  if (idx < 0) return undefined
  const node = nodes[idx] as AssistantMessageNode
  const steps = nodes
    .filter((n): n is AssistantMessageNode => n.kind === 'assistant' && n.turn === node.turn)
    .sort((a, b) => a.step - b.step)
  // Tool results carry no turn number: the turn's are the ones logged after
  // its first step and before its final reply.
  const seqOf = (n: unknown): number | undefined => (n as { seq?: number }).seq
  const firstSeq = seqOf(steps[0])
  const lastSeq = seqOf(node)
  const toolResults = firstSeq === undefined || lastSeq === undefined
    ? []
    : nodes
      .filter((n): n is ToolResultNode => n.kind === 'tool-result' && n.seq > firstSeq && n.seq < lastSeq)
      .sort((a, b) => a.seq - b.seq)
  // The prompt is the last human message before this turn's first node;
  // steering and plugin-injected context also carry kind 'user', so take the
  // FIRST user node of the turn: walk back past this turn's nodes and take
  // the user node nearest the previous turn.
  let prompt: string | undefined
  let prevTurnMaxSeq: number | undefined
  for (let i = idx - 1; i >= 0; i--) {
    const n = nodes[i]
    if (n === undefined) continue
    if ('turn' in n && typeof n.turn === 'number' && n.turn < node.turn) {
      prevTurnMaxSeq = (n as { seq: number }).seq
      break
    }
    if (n.kind === 'user') prompt = userText(n)
  }
  return { node, steps, toolResults, prompt, prevTurnMaxSeq }
}

function fmt(v: unknown): string {
  if (v === undefined || v === null || v === '') return '—'
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  return String(v)
}

function shortHex(v: string | undefined, keep = 12): string {
  if (v === undefined) return '—'
  return v.length > keep * 2 + 1 ? `${v.slice(0, keep)}…${v.slice(-6)}` : v
}

function Row({ k, v, mono }: { k: string; v: unknown; mono?: boolean }) {
  if (v === undefined || v === null || v === '') return null
  return (
    <>
      <dt>{k}</dt>
      <dd className={mono ? `${dialogCss.route} ${css.mono}` : dialogCss.route}>{fmt(v)}</dd>
    </>
  )
}

/**
 * The reproducibility pill + dialog for one finalized assistant message.
 * @param props - message id, the Chat selector hook, locale, and the replay verb.
 * @returns nothing while the message carries no block (older sessions, other providers).
 */
export function PrivasysReproducibilityAction({ messageId, sessionId, useChat, replay, t }: Props) {
  const nodes = useChat(s => s.legacy.nodes)
  const located = useMemo(() => locate(nodes, messageId), [nodes, messageId])
  const turn = located?.node.turn
  const turnEnd = useChat(s => (turn === undefined ? undefined : s.legacy.turnEnds.get(turn - 1)))
  const repro = located === undefined ? undefined : reproOf(located.node.usage)
  const { open, setOpen, rootRef, panelRef, pos } = useStatDialog()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)
  const [copied, setCopied] = useState(false)
  const [replyVerdict, setReplyVerdict] = useState<'match' | 'differs' | undefined>(undefined)

  // A replayed reply judges itself against the hash kept when the replay was
  // armed (this browser, this session id). Never claims a match it cannot show.
  const replayInfo = repro?.harness?.replay
  const replyText = located === undefined ? '' : assistantText(located.node)
  useEffect(() => {
    if (replayInfo === undefined || located === undefined) return
    const stored = readStoredReplay(sessionId)
    if (stored === undefined || stored.of.turn !== located.node.turn) return
    let cancelled = false
    void sha256Hex(replyText).then((hex) => {
      if (!cancelled) setReplyVerdict(hex === stored.reply_sha256 ? 'match' : 'differs')
    })
    return () => { cancelled = true }
  }, [replayInfo, replyText, sessionId, located?.node.turn])

  if (located === undefined || repro === undefined) return null

  const stepsRecorded = located.steps.map(n => reproOf(n.usage))
  const recorded = located.prompt !== undefined
    && stepsRecorded.every(b => b !== undefined && b.harness !== undefined)
  // Reproducibility is a property of PINNED sessions: a pinned call runs
  // strict (a single-use KV-cache salt, so the whole prompt is prefilled
  // fresh), and strict against strict is byte-identical on a batch-
  // invariant engine. A reply served from the shared prefix cache is not:
  // the cached prefix ends where an earlier turn happened to end, and the
  // linear-attention scan of the rest is not invariant to that boundary
  // (prod, 2026-09-10: same seed, prompt and clock, 6288 cached tokens on
  // the original, a different reply on the cold replay). So a replay is
  // offered only for a turn whose every call ran strict.
  const strict = stepsRecorded.every(b => b !== undefined && b.kv_cache_mode === 'strict')
  const replayable = recorded && strict
  const unavailableReason = !recorded
    ? t('message.repro.replayUnavailable')
    : t('message.repro.replayNotStrict')
  const prevTurnEndSeq = turnEnd ?? located.prevTurnMaxSeq

  const onReplay = async (): Promise<void> => {
    if (!replayable || located.prompt === undefined) return
    setBusy(true)
    setError(undefined)
    try {
      await replay({
        messageId,
        turn: located.node.turn,
        prevTurnEndSeq: located.node.turn <= 1 ? undefined : prevTurnEndSeq,
        prompt: located.prompt,
        replyText,
        tools: await replayToolsOf(located.toolResults),
        steps: stepsRecorded.map((b) => {
          const block = b as ReproBlock
          const h = block.harness as HarnessAnnotation
          return {
            ...(block.seed === undefined ? {} : { seed: block.seed }),
            ...(block.temperature === undefined ? {} : { temperature: block.temperature }),
            ...(block.top_p === undefined ? {} : { top_p: block.top_p }),
            ...(block.top_k === undefined ? {} : { top_k: block.top_k }),
            ...(block.max_tokens === undefined ? {} : { max_tokens: block.max_tokens }),
            ...(block.dynamic_context === undefined ? {} : { dynamic_context: block.dynamic_context }),
            ...(h.time_context === undefined ? {} : { time_context: h.time_context }),
            ...(h.prompt_digest === undefined ? {} : { prompt_digest: h.prompt_digest }),
            messages_count: h.messages_count ?? 0,
            tools_count: h.tools_count ?? 0,
          }
        }),
      })
      setOpen(false)
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const onCopy = async (): Promise<void> => {
    const ok = await writeClipboard(JSON.stringify(repro, null, 2))
    if (ok) {
      setCopied(true)
      setTimeout(() => { setCopied(false) }, 1500)
    }
  }

  const label = repro.seed === undefined
    ? t('message.repro.pillNoSeed')
    : t('message.repro.pill', { seed: String(repro.seed) })

  return (
    <span ref={rootRef} className={pillCss.root}>
      <button
        type="button"
        className={pillCss.trigger}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => { setOpen(!open) }}
      >
        <ReproIcon />
        <span className={pillCss.label}>{label}</span>
      </button>
      {open && createPortal(
        <div
          ref={panelRef}
          className={`${dialogCss.panel} ${css.panel}`}
          role="dialog"
          aria-label={t('message.repro.title')}
          style={pos ?? MEASURE_STYLE}
        >
          <div className={dialogCss.title}>
            <span className={dialogCss.titleLabel}>
              <ReproIcon />
              {t('message.repro.title')}
            </span>
            <span className={dialogCss.titleValue}>{repro.model ?? ''}</span>
          </div>
          <div className={dialogCss.titleRule} aria-hidden />
          <p className={css.hint}>{t('message.repro.hint')}</p>

          {replayInfo !== undefined && (
            <ReplayVerdict info={replayInfo} reply={replyVerdict} t={t} />
          )}

          <div className={css.section}>{t('message.repro.request')}</div>
          <dl className={dialogCss.details}>
            <Row k={t('message.repro.seed')} v={repro.seed} />
            <Row k={t('message.repro.temperature')} v={repro.temperature} />
            <Row k={t('message.repro.topP')} v={repro.top_p} />
            <Row k={t('message.repro.topK')} v={repro.top_k} />
            <Row k={t('message.repro.maxTokens')} v={repro.max_tokens} />
            {located.steps.length > 1 && (
              <Row k={t('message.repro.steps')} v={String(located.steps.length)} />
            )}
          </dl>

          <div className={css.section}>{t('message.repro.model')}</div>
          <dl className={dialogCss.details}>
            <Row k={t('message.repro.quantization')} v={repro.quantization} />
            <Row k={t('message.repro.vllm')} v={repro.vllm_version} />
            <Row k={t('message.repro.cuda')} v={repro.cuda_version} />
            <Row k={t('message.repro.gpu')} v={repro.gpu} />
            <Row k={t('message.repro.tee')} v={repro.tee_type} />
            <Row k={t('message.repro.imageDigest')} v={shortHex(repro.image_digest)} mono />
            <Row k={t('message.repro.kvCache')} v={repro.kv_cache_mode} />
            <Row k={t('message.repro.batchInvariance')} v={repro.batch_invariance} />
          </dl>

          <div className={css.section}>{t('message.repro.prompt')}</div>
          <dl className={dialogCss.details}>
            <Row k={t('message.repro.promptDigest')} v={shortHex(repro.harness?.prompt_digest)} mono />
            <Row k={t('message.repro.dynamicContext')} v={repro.dynamic_context} />
          </dl>

          <div className={css.footer}>
            <Button
              variant="outline"
              size="sm"
              disabled={busy || !replayable}
              title={replayable ? undefined : unavailableReason}
              onClick={() => { void onReplay() }}
            >
              {busy ? t('message.repro.replaying') : t('message.repro.replay')}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => { void onCopy() }}>
              {copied ? t('message.repro.copied') : t('message.repro.copy')}
            </Button>
          </div>
          {!replayable && <p className={css.hint}>{unavailableReason}</p>}
          {error !== undefined && <div className={css.error}>{error}</div>}
        </div>,
        document.body,
      )}
    </span>
  )
}

function ReplayVerdict({ info, reply, t }: { info: NonNullable<HarnessAnnotation['replay']>; reply: 'match' | 'differs' | undefined; t: T }) {
  const parts: { text: string; tone: 'good' | 'bad' | 'muted' }[] = []
  if (info.skipped !== undefined) {
    // 'beyond': the model took more steps than the record has; this call's
    // seed is fresh. Anything else: the call's shape was not the recorded one.
    parts.push(info.skipped === 'beyond'
      ? { text: t('message.repro.replyBeyond'), tone: 'bad' }
      : { text: t('message.repro.replySkipped'), tone: 'muted' })
  } else {
    parts.push(info.prompt_match === true
      ? { text: t('message.repro.promptMatch'), tone: 'good' }
      : { text: t('message.repro.promptDiffers'), tone: 'bad' })
    // The clock Confidential AI stamps is the one prompt element the
    // digest cannot see; the trailer says whether the recorded one was used.
    if (info.dynamic_context_match !== undefined) {
      parts.push(info.dynamic_context_match
        ? { text: t('message.repro.clockMatch'), tone: 'good' }
        : { text: t('message.repro.clockDiffers'), tone: 'bad' })
    }
    if (reply !== undefined) {
      parts.push(reply === 'match'
        ? { text: t('message.repro.replyMatch'), tone: 'good' }
        : { text: t('message.repro.replyDiffers'), tone: 'bad' })
    }
  }
  if (info.tools_replayed !== undefined && info.tools_replayed > 0) {
    parts.push({ text: t('message.repro.toolsReplayed', { count: String(info.tools_replayed) }), tone: 'muted' })
  }
  return (
    <div className={css.verdict}>
      <span>{t('message.repro.replayOf', { turn: String(info.of?.turn ?? '?') })}</span>
      {info.step !== undefined && info.step > 0 && (
        <span className={css.muted}>{t('message.repro.replayStep', { step: String(info.step) })}</span>
      )}
      {parts.map(p => (
        <span key={p.text} className={p.tone === 'good' ? css.good : p.tone === 'bad' ? css.bad : css.muted}>
          {p.text}
        </span>
      ))}
    </div>
  )
}

/** Seed glyph: a die face on the 16-grid, at the icon set's stroke weight. */
function ReproIcon() {
  return (
    <svg width={15} height={15} viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
      <rect x="2.15" y="2.15" width="11.7" height="11.7" rx="2.6" stroke="currentColor" strokeWidth="1.3" />
      <circle cx="5.6" cy="5.6" r="1" fill="currentColor" />
      <circle cx="10.4" cy="5.6" r="1" fill="currentColor" />
      <circle cx="8" cy="8" r="1" fill="currentColor" />
      <circle cx="5.6" cy="10.4" r="1" fill="currentColor" />
      <circle cx="10.4" cy="10.4" r="1" fill="currentColor" />
    </svg>
  )
}

/**
 * Replay choreography, run from apply.ts's inject face with the client
 * context: fork the session before the turn (or start a fresh session for
 * turn 1), arm the proxy with the recorded calls, remember the reply's hash
 * locally, open the child and send the prompt again.
 * @param ctx - client root context (sessions service).
 * @param sessionId - the session the turn lives in.
 * @param request - the recorded turn.
 */
export async function replayTurn(ctx: Context, sessionId: SessionId, request: ReplayRequest): Promise<void> {
  // Turn 1 has nothing to fork from, so a new session opens: in the SAME
  // Workspace as the recorded one. The workspace shapes the prompt (its
  // instructions, the paths the model is told about), so a session
  // elsewhere would be a different prompt before the first token. The
  // browser groups sessions by Workspace MEMBERSHIP, not by directory: a
  // session created with a bare cwd lands under Ungrouped even in the
  // same directory (seen 2026-09-10), so the membership is what is passed,
  // and the directory only when the source belongs to no Workspace.
  const workspaceId = ctx.workspaces.list.getSnapshot().items
    .find(w => w.sessionIds.includes(sessionId))?.workspaceId
  const cwd = ctx.sessions.list.getSnapshot().byId[sessionId]?.cwd
  const child = request.prevTurnEndSeq === undefined
    ? await ctx.sessions.create(
      workspaceId !== undefined ? { workspaceId } : cwd === undefined ? {} : { cwd },
    )
    : await ctx.sessions.fork({ sessionId, atSeq: request.prevTurnEndSeq, increaseTitle: true })
  await pvSendJson('PUT', `/privasys/sampling?session=${encodeURIComponent(child)}`, {
    replay: {
      of: { session: sessionId, message_id: request.messageId, turn: request.turn },
      steps: request.steps,
      ...(request.tools.length === 0 ? {} : { tools: request.tools }),
    },
  })
  try {
    const stored: StoredReplay = {
      of: { session: sessionId, message_id: request.messageId, turn: request.turn },
      reply_sha256: await sha256Hex(request.replyText),
    }
    localStorage.setItem(REPLAY_STORE_PREFIX + child, JSON.stringify(stored))
  } catch {
    // No local record: the panel will show the prompt verdict only.
  }
  ctx.sessions.open(child)
  const deadline = Date.now() + 15_000
  let binding = ctx.sessions.binding(child)
  while (binding === undefined && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 200))
    binding = ctx.sessions.binding(child)
  }
  if (binding === undefined) throw new Error('the replay session did not open in time')
  const result = await binding.session.prompt([{ type: 'text', text: request.prompt }], 'queue')
  if (!result.ok) throw new Error(`${result.error.code}: ${result.error.message}`)
}
