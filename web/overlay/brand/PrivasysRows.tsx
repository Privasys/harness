/**
 * Privasys sidebar-foot rows (attested-harness fork).
 *
 * ONE occupant of the `sidebar.footer.action` list slot renders the column of
 * rows that sit above dsh's Settings seat: today the Attestation row. dsh lays
 * list occupants out in a row and expects each to own its button geometry
 * (ui-sidebar contract), so a single occupant with a column is the seam as
 * designed: no sidebar source is edited and no container style is overridden.
 *
 * The Settings seat itself (settings.launcher) is ours too, and lives with the
 * settings shell in ui-settings-general: the storage row (only while setup is
 * incomplete), Settings, and the signed-in user below it.
 *
 * Every row is the same control as dsh's Settings trigger and Cordis badge
 * (PrivasysFootRow.tsx + PrivasysFoot.module.css).
 */
import type { SidebarFooterActionOwnerProps } from '@deepseek-ai/dsh-client-ui-sidebar/client'
import { PrivasysAttestationRow } from './PrivasysAttestation.tsx'
import css from './PrivasysFoot.module.css'

/** The column of Privasys rows: the single `sidebar.footer.action` occupant. */
export function PrivasysFootRows({ wide }: SidebarFooterActionOwnerProps) {
  return (
    <div className={wide ? css.column : `${css.column} ${css.rail}`}>
      <PrivasysAttestationRow wide={wide} />
    </div>
  )
}
