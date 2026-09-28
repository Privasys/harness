/**
 * Privasys: the Connectors chip in the composer (attested-harness fork).
 *
 * Which of the deployment's attested connectors this conversation may use,
 * grouped by category, one switch each. It reads the host's `connectors`
 * session projection (app/privasys-connectors.mjs) and writes through the
 * host's `/connectors <server> on|off` command, the way dsh's own permission
 * picker reads its projection and writes through `/permission`: the pushed
 * projection frame is the one confirmation.
 *
 * Placed in the permission-presets client package because that package
 * already carries every dependency a composer control needs, and the image
 * installs before the overlay with a frozen lockfile. It names nothing: the
 * connectors, their labels and categories are the host's.
 */
import { useState } from 'react'
import clsx from 'clsx'
import { IconChevronDownOutlineRegular, Menu } from '@deepseek-ai/dsh-client-ui-primitives'
import type { MenuEntry } from '@deepseek-ai/dsh-client-ui-primitives'
import type { InjectFace, PropsRuntime } from '@deepseek-ai/dsh-client-ui-slots'
// Type-only: the conversation-owned composer slot declarations.
import type {} from '@deepseek-ai/dsh-client-ui-conversation/client'
import css from './PermissionSelect.module.css'

/** One connector as the host's projection shows it. */
interface Connector {
  server: string
  label: string
  category: string
  on: boolean
}

interface ConnectorsView {
  connectors: Connector[]
}

/** Business face injected by the slot registration. */
export interface PrivasysConnectorsInjected {
  /** Switch one connector for this session through the host command. */
  toggle: (server: string, on: boolean) => Promise<boolean>
}

export type PrivasysConnectorsProps =
  PropsRuntime<'conversation.input.left'>
  & InjectFace<PrivasysConnectorsInjected>

export function PrivasysConnectors(props: PrivasysConnectorsProps) {
  const { toggle } = props
  // The projection key is the host plugin's, not in dsh's typed map.
  const useProjection = (props as unknown as { useProjection: (key: string) => unknown }).useProjection
  const view = useProjection('connectors') as ConnectorsView | undefined
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)

  if (view === undefined || view.connectors.length === 0) return null
  const connectors = view.connectors
  const on = connectors.filter(c => c.on)

  // One heading per category, in the order the host lists them.
  const groups: { category: string; connectors: Connector[] }[] = []
  for (const c of connectors) {
    const g = groups.find(x => x.category === c.category)
    if (g === undefined) groups.push({ category: c.category, connectors: [c] })
    else g.connectors.push(c)
  }
  const items: MenuEntry[] = []
  for (const g of groups) {
    if (g.category !== '') items.push({ type: 'label', id: `category:${g.category}`, text: g.category })
    for (const c of g.connectors) items.push({ id: c.server, label: c.label, disabled: busy !== null })
  }

  const choose = (server: string): void => {
    const c = connectors.find(x => x.server === server)
    if (c === undefined || busy !== null) return
    setBusy(server)
    void toggle(server, !c.on)
      .catch(() => false)
      .then(() => { setBusy(null) })
  }

  const label = `Connectors ${on.length}/${connectors.length}`
  return (
    <Menu
      open={open}
      items={items}
      selectedIds={on.map(c => c.server)}
      selection="check"
      onSelect={choose}
      onClose={() => { setOpen(false) }}
      side="top"
      portal
      anchor={
        <button
          type="button"
          className={css.trigger}
          aria-label={label}
          title="Which connectors this conversation may use"
          disabled={busy !== null}
          onClick={() => { setOpen(!open) }}
        >
          <span className={css.triggerLabel}>{label}</span>
          <span className={clsx(css.chevron, open && css.chevronOpen)} aria-hidden>
            <IconChevronDownOutlineRegular />
          </span>
        </button>
      }
    />
  )
}
