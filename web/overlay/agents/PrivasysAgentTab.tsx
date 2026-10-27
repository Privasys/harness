/**
 * The agent page (attested-harness fork): a right-sidebar tab type, `agent`,
 * showing the agent the current session belongs to as the chat built it:
 * its instructions (agent.md), what starts it and how often it may run
 * (agent.yaml), and the pause switch. Changing what an agent does is a
 * conversation, so the page says so and leaves the folder to the chat.
 *
 * A page type, registered the way dsh's own files tab is (definition in
 * `ctx.sidebarRightTabs`, body and title under the keyed seats): additive,
 * no dsh source edited, so it survives a dsh upgrade untouched.
 */
import { useState, useSyncExternalStore } from 'react'
import type { ReactNode } from 'react'
import {
  Button, GuideArtworkFiles, IconAgentPresetOutlineRegular, IconRefreshOutlineRegular, Tooltip,
} from '@deepseek-ai/dsh-client-ui-primitives'
import type { PropsRuntime } from '@deepseek-ai/dsh-client-ui-slots'
import type { SidebarRightTabDefinition } from '@deepseek-ai/dsh-client-ui-sidebar-right/client'
import { agentAt, refresh, setPaused, snapshot, subscribe, type AgentItem } from './privasys-agents.ts'
import css from './PrivasysAgent.module.css'

/** The tab kind, and what `openTab` names. */
export const AGENT_KIND = 'agent'

/** This implementation's identity in the tab system, and the key its body registers under. */
export const AGENT_ID = '@privasys/dsh-client-ui-agent'

export function agentDefinition(): SidebarRightTabDefinition {
  return {
    id: AGENT_ID,
    kind: AGENT_KIND,
    priority: 'builtin',
    title: () => 'Agent',
    guide: [{
      id: 'agent',
      order: 5,
      title: () => 'Agent',
      description: () => 'Who this agent is, what starts it, and whether it is paused',
      icon: GuideArtworkFiles,
    }],
  }
}

/** What starts the agent, in a sentence. */
function startsWhen(a: AgentItem): string {
  const t = a.trigger ?? {}
  if (t.every !== undefined) return `Runs every ${t.every}.`
  if (t.at !== undefined) return `Runs on the schedule "${t.at}" (UTC).`
  if (t.on !== undefined) return `Runs when something arrives on ${t.on.replace('.', ': ')}.`
  return 'Runs only when you ask it to, in this workspace.'
}

function AgentCard({ agent }: { agent: AgentItem }): ReactNode {
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState('')
  const triggered = agent.trigger !== undefined && Object.keys(agent.trigger).length > 0
  const flip = async (): Promise<void> => {
    setBusy(true)
    setProblem('')
    const error = await setPaused(agent.name, !agent.paused)
    setBusy(false)
    if (error !== '') setProblem(error)
  }
  return (
    <>
      <h2 className={css.title}>{agent.name}</h2>
      <p className={css.status}>
        {agent.paused ? 'Paused. ' : ''}
        {startsWhen(agent)}
        {triggered ? ` After a burst, it waits ${agent.debounce}; it never runs more often than every ${agent.min_interval}, one run at a time.` : ''}
      </p>
      {triggered && (
        <div className={css.actions}>
          <Button variant={agent.paused ? 'primary' : 'outline'} size="sm" disabled={busy} onClick={() => { void flip() }}>
            {busy ? 'Changing…' : agent.paused ? 'Resume' : 'Pause'}
          </Button>
          {problem !== '' && <span className={css.warn}>{problem}</span>}
        </div>
      )}
      {agent.prompt !== undefined && agent.prompt !== '' && (
        <section className={css.section}>
          <h3 className={css.heading}>What a run does</h3>
          <p className={css.text}>{agent.prompt}</p>
        </section>
      )}
      <section className={css.section}>
        <h3 className={css.heading}>Instructions</h3>
        <pre className={css.pre}>{agent.instructions === '' ? '(none yet)' : agent.instructions}</pre>
      </section>
      <details className={css.section}>
        <summary className={css.heading}>Definition (agent.yaml)</summary>
        <pre className={css.pre}>{agent.definition === '' ? '(none yet)' : agent.definition}</pre>
      </details>
      <p className={css.muted}>
        To change this agent, ask in the chat: it rewrites these files in your
        Drive. Its runs appear as sessions in this workspace.
      </p>
    </>
  )
}

/** The body: the agent whose folder this session runs in. */
export function PrivasysAgentBody({ sessionId, useSessions }: PropsRuntime<'sidebar.right.pane.tab'>): ReactNode {
  const cwd = useSessions(sessions => sessions.byId[sessionId]?.cwd)
  const view = useSyncExternalStore(subscribe, snapshot)
  const agent = agentAt(view.agents, cwd)
  return (
    <div className={css.body}>
      <div className={css.toolbar}>
        <Tooltip label="Reload" side="bottom" delayMs={500}>
          <button type="button" className={css.iconButton} aria-label="Reload" onClick={() => { void refresh() }}>
            <IconRefreshOutlineRegular size={14} />
          </button>
        </Tooltip>
      </div>
      {!view.loaded && <p className={css.muted}>Reading your agents…</p>}
      {view.loaded && view.problem !== '' && <p className={css.warn}>{view.problem}</p>}
      {view.loaded && view.problem === '' && agent !== undefined && <AgentCard agent={agent} />}
      {view.loaded && view.problem === '' && agent === undefined && (
        <>
          <h2 className={css.title}>Not an agent</h2>
          <p className={css.text}>
            This session is not inside an agent's folder. To build one, open the
            Agents header's add menu and pick "New agent…", or ask here: "make me an
            agent that…".
          </p>
          {view.agents.length > 0 && (
            <section className={css.section}>
              <h3 className={css.heading}>Your agents</h3>
              <ul className={css.list}>
                {view.agents.map(a => (
                  <li key={a.name}>{a.name}{a.paused ? ' (paused)' : a.scheduled ? '' : ' (on request)'}</li>
                ))}
              </ul>
            </section>
          )}
        </>
      )}
    </div>
  )
}

/** The chip title: the agent glyph before the type's label. */
export function PrivasysAgentTitle({ useTabInfo }: PropsRuntime<'sidebar.right.pane.tab.title'>): ReactNode {
  const { tab } = useTabInfo()
  return (
    <>
      <IconAgentPresetOutlineRegular size={16} className={css.titleIcon} />
      {tab.title}
    </>
  )
}
