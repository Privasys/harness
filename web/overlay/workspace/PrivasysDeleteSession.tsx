/**
 * Privasys: "Delete session" as a row-menu entry (attested-harness fork).
 *
 * Archiving hides a session; deleting removes it for good, here and from the
 * holder's Drive. dsh 0.1.7 makes the row menu a slot whose entries are
 * components like this one, so the entry is registered beside the shipped
 * ones (index.ts) instead of patched into a menu array.
 *
 * The entry publishes on a module-level bus rather than acting: the
 * confirmation and the call live at the browser root, where a dialog can
 * survive the menu closing under it (PrivasysSessionDelete.ts).
 */
import { IconTrashOutlineRegular, MenuItemButton } from '@deepseek-ai/dsh-client-ui-primitives'
import type { SessionMenuItemProps } from '../contract/slots.ts'
import { requestSessionDelete } from '../rows/PrivasysSessionDelete.ts'

/**
 * The menu entry.
 * @param props - the row's share (session and its display title), the menu's open state, and the locale seat.
 * @returns the entry.
 */
export function PrivasysDeleteSessionMenuItem({
  sessionId, displayTitle, useMenuOpenState, t,
}: SessionMenuItemProps) {
  const [, setMenuOpen] = useMenuOpenState()
  return (
    <MenuItemButton
      icon={<IconTrashOutlineRegular />}
      danger
      onSelect={() => {
        setMenuOpen(false)
        requestSessionDelete(sessionId, displayTitle)
      }}
    >
      {t('menu.deleteSession')}
    </MenuItemButton>
  )
}
