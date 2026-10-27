/**
 * "New agent…" (attested-harness fork): the one gesture that starts building
 * an agent, from the Agents header's add menu.
 *
 * An agent is a folder of text the chat writes (README, "Building an agent
 * on this harness", contract 2), so creating one IS a conversation: a new
 * session whose first message invokes the agent-builder skill. dsh treats a
 * user message beginning with `/<name>` as a deterministic skill invocation
 * (tool-skill), so the draft starts with that token and the holder adds what
 * the agent should do before sending.
 *
 * The picker's menu entry publishes a request; the browser, which owns
 * navigation, closes the menu and starts the session. The same seam as
 * PrivasysSessionDelete.ts: the two components are in one package, and a
 * module-level bus keeps dsh's slot contract untouched.
 */

/** The skill the image ships for building agents (agent-skills/agent-builder). */
export const AGENT_BUILDER_SKILL = 'agent-builder'

/** The draft a new-agent session opens with; the holder completes the line. */
export const NEW_AGENT_DRAFT = `/${AGENT_BUILDER_SKILL} `

type Listener = () => void

const listeners = new Set<Listener>()

/** Ask the browser to start a new-agent session. */
export function requestNewAgent(): void {
  for (const listener of listeners) listener()
}

/** Subscribe to new-agent requests; returns the unsubscribe. */
export function onNewAgentRequest(listener: Listener): () => void {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}
