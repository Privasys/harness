/**
 * MCP elicitation on dsh's own question surface (attested-harness fork).
 *
 * A tool app behind the measured proxy may need the person's answer before
 * it can act: a connector without a credential yet, a service that must be
 * set up once. The model must not collect it, because the model would carry
 * it in its context and the session record would keep it. MCP's standard
 * step for this is elicitation: the server asks the client for structured
 * input against a JSON schema, the client's own UI collects it, and the
 * answer goes back to the server. This file is the client half: the request
 * arrives on the MCP connection (the official client's `elicitation/create`
 * handler), is routed to the conversation it belongs to (the proxy echoes
 * the session id in `_meta`), rendered through `ctx.userQuestions`, and the
 * answers are coerced back to the schema's types and returned. The model
 * sees the tool's final result and nothing else; the answers are not part
 * of the session.
 *
 * Generic on purpose: every property of the schema becomes a question,
 * enums become options, `format: "password"` becomes a masked field, and
 * the first question carries a banner saying which tool asks and why the
 * form is separate from the conversation.
 */
import type { Context } from '@deepseek-ai/cordis'
import type { ClientContext, ElicitRequest, ElicitResult } from '@modelcontextprotocol/client'

/** The subset of the schema this surface renders. */
interface PropertySchema {
  type?: string
  title?: string
  description?: string
  default?: unknown
  format?: string
  enum?: string[]
  enumNames?: string[]
  oneOf?: { const: string; title?: string }[]
  items?: { enum?: string[]; anyOf?: { const: string; title?: string }[] }
  minItems?: number
  maxItems?: number
}

/** dsh's question item plus the field this fork adds (it rides the wire untouched). */
interface Question {
  id: string
  question: string
  detail?: string
  header?: string
  options?: { label: string; description?: string }[]
  multiSelect?: boolean
  /** Rendered as a masked input; never echoed. */
  secret?: boolean
}

interface AnswerItem {
  id: string
  selected: string[]
  custom?: string
}

interface UserQuestionsService {
  ask(request: { questions: Question[]; agent?: unknown; signal?: AbortSignal }): Promise<{ answers: AnswerItem[] }>
}

interface AgentsService {
  get(id: string): { id: string } | undefined
  roots(): { id: string }[]
}

/** Banner shown above the form, in the first question's detail. */
function banner(serverName: string, message: string): string {
  return `**This form comes from the \`${serverName}\` tool, not from the assistant.** `
    + 'Your answers go to that service only: they are not written into this conversation, '
    + 'its record, or anything the assistant reads. Masked fields are never shown back.\n\n'
    + message
}

/** Turn one schema property into one question. */
function questionFor(id: string, prop: PropertySchema, required: boolean, secret: boolean): Question {
  const q: Question = { id, question: prop.title ?? id }
  const parts: string[] = []
  if (prop.description !== undefined) parts.push(prop.description)
  if (prop.default !== undefined && !secret) parts.push(`Default: ${String(prop.default)}`)
  if (!required) parts.push('Optional.')
  if (parts.length > 0) q.detail = parts.join(' ')
  const single = prop.enum ?? prop.oneOf?.map(o => o.const)
  const multi = prop.items?.enum ?? prop.items?.anyOf?.map(o => o.const)
  if (prop.type === 'boolean') {
    q.options = [{ label: 'Yes' }, { label: 'No' }]
  } else if (prop.type === 'array' && multi !== undefined) {
    q.options = multi.map((value, i) => ({ label: value, ...(prop.items?.anyOf?.[i]?.title === undefined ? {} : { description: prop.items.anyOf[i].title }) }))
    q.multiSelect = true
  } else if (single !== undefined) {
    q.options = single.map((value, i) => {
      const title = prop.oneOf?.[i]?.title ?? prop.enumNames?.[i]
      return { label: value, ...(title === undefined ? {} : { description: title }) }
    })
  }
  if (secret || prop.format === 'password') q.secret = true
  return q
}

/** Coerce one answer back to the property's declared type; undefined = unanswered. */
function valueFor(prop: PropertySchema, item: AnswerItem | undefined): string | number | boolean | string[] | undefined {
  if (item === undefined) return undefined
  const text = item.custom?.trim() ?? ''
  if (prop.type === 'boolean') {
    const picked = item.selected[0] ?? text
    if (picked === '') return undefined
    return /^(yes|true|y|1)$/i.test(picked)
  }
  if (prop.type === 'array') {
    return item.selected.length > 0 ? item.selected : (text === '' ? undefined : [text])
  }
  const picked = item.selected[0] ?? (text === '' ? undefined : text)
  if (picked === undefined) return undefined
  if (prop.type === 'number' || prop.type === 'integer') {
    const n = Number(picked)
    return Number.isFinite(n) ? n : undefined
  }
  return picked
}

/**
 * Handle one `elicitation/create` from a server.
 * @param ctx - the MCP client plugin's context, used to reach the agents and question services.
 * @param serverName - the configured MCP server name, shown in the banner.
 * @param request - the validated request.
 * @param call - the client's handler context; its `_meta.privasysSession` names the conversation.
 * @returns accept with the typed answers, decline when it cannot be shown, cancel when the person closes it.
 */
export async function privasysElicit(ctx: Context, serverName: string, request: ElicitRequest, call: ClientContext): Promise<ElicitResult> {
  const params = request.params as { mode?: string; message: string; requestedSchema?: { properties?: unknown; required?: string[] } }
  if (params.mode === 'url' || params.requestedSchema === undefined) {
    ctx.logger.warn(`${serverName}: only form-mode elicitation is supported here`)
    return { action: 'decline' }
  }
  const get = (name: string): unknown => (ctx as unknown as { get(name: string): unknown }).get(name)
  const agents = get('agents') as AgentsService | undefined
  const userQuestions = get('userQuestions') as UserQuestionsService | undefined
  if (agents === undefined || userQuestions === undefined) {
    ctx.logger.warn(`${serverName}: no question surface in this process; elicitation declined`)
    return { action: 'decline' }
  }
  const meta = (call.mcpReq._meta ?? {}) as { privasysSession?: unknown; privasysSecrets?: unknown }
  const sessionId = typeof meta.privasysSession === 'string' ? meta.privasysSession : undefined
  // Which properties are secrets. The client validates the schema strictly
  // and knows no password format, so the proxy lifts the mark into _meta.
  const secrets = new Set(Array.isArray(meta.privasysSecrets) ? meta.privasysSecrets.filter((s): s is string => typeof s === 'string') : [])
  let agent = sessionId === undefined ? undefined : agents.get(sessionId)
  if (agent === undefined || !agents.roots().includes(agent)) {
    // Without a routable session, the one live root conversation is the
    // only honest guess; more than one, and the question could land in the
    // wrong window, so it is declined instead.
    const roots = agents.roots()
    agent = roots.length === 1 ? roots[0] : undefined
  }
  if (agent === undefined) {
    ctx.logger.warn(`${serverName}: elicitation for an unknown session (${sessionId ?? 'none'}); declined`)
    return { action: 'decline' }
  }

  const schema = params.requestedSchema
  const properties = (schema.properties ?? {}) as Record<string, PropertySchema>
  const required = new Set(schema.required ?? [])
  const questions = Object.entries(properties).map(([id, prop]) => questionFor(id, prop, required.has(id), secrets.has(id)))
  if (questions.length === 0) return { action: 'accept', content: {} }
  const first = questions[0] as Question
  first.header = `Question from the ${serverName} tool`
  first.detail = banner(serverName, params.message) + (first.detail === undefined ? '' : `\n\n${first.detail}`)

  let answers: AnswerItem[]
  try {
    const result = await userQuestions.ask({ questions, agent, signal: call.mcpReq.signal })
    answers = result.answers
  } catch (error: unknown) {
    const code = (error as { code?: string }).code
    if (code === 'ASK_CANCELLED' || code === 'ASK_ABORTED') return { action: 'cancel' }
    ctx.logger.warn(`${serverName}: elicitation could not be shown: ${error instanceof Error ? error.message : String(error)}`)
    return { action: 'decline' }
  }
  const byId = new Map(answers.map(a => [a.id, a]))
  const content: Record<string, string | number | boolean | string[]> = {}
  for (const [id, prop] of Object.entries(properties)) {
    const value = valueFor(prop, byId.get(id))
    if (value !== undefined) {
      content[id] = value
    } else if (prop.default !== undefined) {
      content[id] = prop.default as string | number | boolean | string[]
    }
  }
  for (const id of required) {
    if (content[id] === undefined) {
      // A required field left empty is not an answer: the person skipped
      // the form, which the tool reads as a decline, not as bad input.
      return { action: 'decline' }
    }
  }
  return { action: 'accept', content }
}
