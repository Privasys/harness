// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.
//
// Privasys routines: the loopback door through which the egress proxy starts
// an UNATTENDED run of one of the holder's agents (plan §3.3, 2026-09-14).
//
// dsh has no scheduler and nothing that wakes a cold session. What it has is
// the webhook runtime: a trusted rule turns a delivery into a NEW root
// session in a named workspace, with a preset and a prompt, committed with
// `followup()`. The workspace is resolved or registered on the way, and the
// session shows in the sidebar live. This plugin is that rule plus the
// smallest possible ingress for it: one exact route on a second web server
// that lives in its own isolated realm (composed by the proxy in the
// per-worker patch), bound to loopback, reachable by the proxy alone because
// every call must carry this worker's ingress token.
//
// The proxy owns the clock and the events (a held `changes` poll on the
// connector, timers for `every`); this file owns nothing but the door.

import { randomUUID } from 'node:crypto'
import { isAbsolute } from 'node:path'
import { WebhookDeliveryId, WebhookRuleId, WebhookSourceId } from '@deepseek-ai/dsh-webhook'

export const name = 'privasys-routines'
export const inject = ['webServer', 'webhookRuntime']

const KIND = 'privasys-routine'
const MAX_BODY = 64 * 1024

export function apply(ctx, config) {
  const token = String(config?.token ?? '')
  if (token.length < 16) throw new Error('privasys-routines: a token of at least 16 characters is required')
  const path = String(config?.path ?? '/privasys/run')
  const agentPreset = String(config?.agentPreset ?? 'standard')
  const permissionPreset = String(config?.permissionPreset ?? 'workspace-write')
  const source = WebhookSourceId(String(config?.source ?? 'privasys-proxy'))

  // The rule: a delivery names the agent's workspace and what to do there.
  ctx.effect(() => ctx.webhookRuntime.register({
    id: WebhookRuleId('privasys-routine-run'),
    kind: KIND,
    run(delivery) {
      if (delivery.source !== source) return null
      const { workspacePath, title, prompt } = delivery.event
      return { workspacePath, title, prompt, agentPreset, permissionPreset }
    },
  }), 'privasys-routines: rule')

  // The door: POST {workspacePath, title, prompt} with the worker's token.
  ctx.effect(() => ctx.webServer.register({
    kind: 'exact',
    path,
    async handler(req, res) {
      if (req.method !== 'POST') return respond(res, 405, 'POST only')
      if (req.headers['x-privasys-routine-token'] !== token) return respond(res, 401, 'no')
      let body = ''
      for await (const chunk of req) {
        body += chunk
        if (body.length > MAX_BODY) return respond(res, 413, 'too large')
      }
      let payload
      try {
        payload = JSON.parse(body)
      } catch {
        return respond(res, 400, 'not JSON')
      }
      const { workspacePath, title, prompt } = payload ?? {}
      if (typeof workspacePath !== 'string' || !isAbsolute(workspacePath)) {
        return respond(res, 400, 'workspacePath must be an absolute path')
      }
      if (typeof title !== 'string' || !title.trim() || typeof prompt !== 'string' || !prompt.trim()) {
        return respond(res, 400, 'title and prompt are required')
      }
      const deliveryId = WebhookDeliveryId(randomUUID())
      try {
        ctx.webhookRuntime.dispatch({
          kind: KIND,
          source,
          deliveryId,
          event: { workspacePath, title: title.trim(), prompt },
          receivedAt: Date.now(),
        })
      } catch (err) {
        return respond(res, 503, `not dispatched: ${err?.message ?? err}`)
      }
      // 202: dispatched, not finished. The runtime is fire-and-forget; the
      // run's session is the record of what happened.
      res.writeHead(202, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ dispatched: true, delivery_id: String(deliveryId) }))
    },
  }), `privasys-routines: ${path}`)
}

function respond(res, status, message) {
  res.writeHead(status, { 'content-type': 'text/plain; charset=utf-8' })
  res.end(message)
}
