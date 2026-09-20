// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.
//
// Privasys routines: the loopback door through which the egress proxy starts
// an UNATTENDED run of one of the holder's agents (proxy routines.go).
//
// dsh has no scheduler and nothing that wakes a cold session. What it has is
// the webhook runtime: a trusted rule turns a delivery into a NEW root
// session in a named workspace, with a preset and a prompt. The workspace is
// resolved or registered on the way, and the session shows in the sidebar
// live. This plugin is that rule plus one exact route on the worker's OWN
// web server. That server is bound to loopback and admits only requests
// carrying the ingress token the proxy minted for this worker (overlay 2b'),
// and the proxy never forwards the door's path from outside, so the proxy is
// the only caller there can be. No second listener, no second token.
//
// The proxy owns the clock and the events; this file owns nothing but the
// door. What a run may use is two presets the proxy names in the body (the
// `routine` composition: the attested tools, file reading and skills, no
// shell; the `routine` permissions: writes inside the workspace, no approval
// asked), with the plugin's config as the fallback: the choice is made in
// the measured Go layer and only read here. dsh refuses a name it has no
// preset for at dispatch, which the door reports.

import { randomUUID } from 'node:crypto'
import { isAbsolute } from 'node:path'
import { WebhookDeliveryId, WebhookRuleId, WebhookSourceId } from '@deepseek-ai/dsh-webhook'

export const name = 'privasys-routines'
export const inject = ['webServer', 'webhookRuntime']

const KIND = 'privasys-routine'
const MAX_BODY = 64 * 1024
const PRESET_NAME = /^[a-z][a-z0-9-]*$/

export function apply(ctx, config) {
  const path = String(config?.path ?? '/privasys/internal/run')
  const defaultAgentPreset = String(config?.agentPreset ?? 'routine')
  const defaultPermissionPreset = String(config?.permissionPreset ?? 'routine')
  const source = WebhookSourceId(String(config?.source ?? 'privasys-proxy'))

  // The rule: a delivery names the agent's workspace, what to do there and
  // under which presets.
  ctx.effect(() => ctx.webhookRuntime.register({
    id: WebhookRuleId('privasys-routine-run'),
    kind: KIND,
    run(delivery) {
      if (delivery.source !== source) return null
      const { workspacePath, title, prompt, agentPreset, permissionPreset } = delivery.event
      return { workspacePath, title, prompt, agentPreset, permissionPreset }
    },
  }), 'privasys-routines: rule')

  // The door: POST {workspacePath, title, prompt, agentPreset?, permissionPreset?}.
  ctx.effect(() => ctx.webServer.register({
    kind: 'exact',
    path,
    async handler(req, res) {
      if (req.method !== 'POST') return respond(res, 405, 'POST only')
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
      const agentPreset = payload.agentPreset ?? defaultAgentPreset
      const permissionPreset = payload.permissionPreset ?? defaultPermissionPreset
      if (typeof agentPreset !== 'string' || !PRESET_NAME.test(agentPreset)
        || typeof permissionPreset !== 'string' || !PRESET_NAME.test(permissionPreset)) {
        return respond(res, 400, 'agentPreset and permissionPreset must be preset names')
      }
      const deliveryId = WebhookDeliveryId(randomUUID())
      try {
        ctx.webhookRuntime.dispatch({
          kind: KIND,
          source,
          deliveryId,
          event: { workspacePath, title: title.trim(), prompt, agentPreset, permissionPreset },
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
