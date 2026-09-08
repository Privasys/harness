/**
 * One Privasys sidebar-foot row (attested-harness fork): the control every
 * Privasys row is built from, so the three rows and dsh's own Settings
 * trigger read as one family. Geometry and tokens live in
 * PrivasysFoot.module.css, copied from dsh's Settings trigger and Cordis
 * badge. In the collapsed rail the row is the 36px circle every rail
 * control uses, with the label as a dsh Tooltip.
 */
import type { ReactNode } from 'react'
import { Tooltip } from '@deepseek-ai/dsh-client-ui-primitives'
import css from './PrivasysFoot.module.css'

/** Shield glyph on dsh's 16-grid, drawn at the icon set's ~1.3px weight. */
export function ShieldIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true">
      <path
        d="M8 1.6 13.3 3.55V7.4c0 3.35-2.25 5.75-5.3 6.95C4.95 13.15 2.7 10.75 2.7 7.4V3.55L8 1.6Z"
        stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round"
      />
      <path d="m5.7 8 1.7 1.7 3.1-3.3" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

export interface FootRowProps {
  wide: boolean
  icon: ReactNode
  label: string
  /** Right-pinned status (dot + word); shown only in the wide column. */
  status?: ReactNode
  /** `title` when wide; the rail shows the label as a tooltip instead. */
  title: string
  ariaLabel?: string
  expanded?: boolean
  haspopup?: 'menu' | 'dialog'
  onClick: () => void
}

/**
 * Render one sidebar-foot row.
 * @param props - row content and the sidebar's wide flag.
 * @returns the row inside its 42px layer (36px in the rail).
 */
export function FootRow({ wide, icon, label, status, title, ariaLabel, expanded, haspopup, onClick }: FootRowProps) {
  const button = (
    <button
      type="button"
      className={css.row}
      title={wide ? title : undefined}
      aria-label={ariaLabel ?? label}
      aria-haspopup={haspopup}
      aria-expanded={expanded}
      onClick={onClick}
    >
      {icon}
      {wide && <span className={css.label}>{label}</span>}
      {wide && status !== undefined && <span className={css.status}>{status}</span>}
    </button>
  )
  return (
    <div className={css.layer}>
      {wide ? button : <Tooltip label={label} side="right" delayMs={300}>{button}</Tooltip>}
    </div>
  )
}
