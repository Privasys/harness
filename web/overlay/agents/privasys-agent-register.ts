/**
 * Registers the agent page (PrivasysAgentTab.tsx) as a right-sidebar tab
 * type, from ui-sidebar-files's own plugin body: the package already holds
 * the registry, the seats and the session scope, and this is the one line
 * its index.ts gains (attested-harness fork).
 */
import type { Context as ClientContext } from '@deepseek-ai/cordis'
import type {} from '@deepseek-ai/dsh-client-ui-sidebar-right/client'
import { AGENT_ID, PrivasysAgentBody, PrivasysAgentTitle, agentDefinition } from './PrivasysAgentTab.tsx'

export function applyPrivasysAgent(ctx: ClientContext): void {
  ctx.effect(() => ctx.sidebarRightTabs.register(agentDefinition()), 'privasys: agent tab type')
  ctx.effect(() => ctx.slots.inject('sidebar.right.pane.tab', () => ctx.slots.register(
    { name: 'sidebar.right.pane.tab', key: AGENT_ID },
    PrivasysAgentBody,
  )), 'privasys: agent tab body')
  ctx.effect(() => ctx.slots.inject('sidebar.right.pane.tab.title', () => ctx.slots.register(
    { name: 'sidebar.right.pane.tab.title', key: AGENT_ID },
    PrivasysAgentTitle,
  )), 'privasys: agent tab title')
}
