/**
 * Privasys sidebar-foot rows (attested-harness fork).
 *
 * ONE occupant of the `sidebar.footer.action` list slot renders a column of
 * three rows — Attestation, Sessions (storage), User — above dsh's own
 * Settings trigger. dsh lays list occupants out in a row and expects each to
 * own its button geometry (ui-sidebar contract), so a single occupant with
 * a column is the seam as designed: no sidebar source is edited and no
 * container style is overridden.
 *
 * Every row is the same control as dsh's Settings trigger and Cordis badge
 * (PrivasysFootRow.tsx + PrivasysFoot.module.css): dsh's own icon set at
 * the same sizes, the primary label tone, and a right-pinned status in the
 * caption tone — a StateDot plus one word — instead of a coloured label.
 * Menus and dialogs are dsh's primitives (Menu, Modal).
 *
 * The User row calls the vanilla auth shell through `window.__PRIVASYS_SHELL__`
 * (privasys-shell.js owns the sealed session and logout); the row is a pure
 * trigger.
 */
import { useRef, useState } from 'react'
import { IconUserOutline16, Menu } from '@deepseek-ai/dsh-client-ui-primitives'
import type { SidebarFooterActionOwnerProps } from '@deepseek-ai/dsh-client-ui-sidebar/client'
import { PrivasysAttestationRow } from './PrivasysAttestation.tsx'
import { PrivasysStorageRow } from './PrivasysStorage.tsx'
import { FootRow } from './PrivasysFootRow.tsx'
import css from './PrivasysFoot.module.css'

interface PrivasysShellHooks {
  logout?: () => void
  /** The signed-in user's Display Name (the `profile`-scope `name` claim). */
  userName?: () => string | undefined
}

function shell(): PrivasysShellHooks {
  return (globalThis as { __PRIVASYS_SHELL__?: PrivasysShellHooks }).__PRIVASYS_SHELL__ ?? {}
}

function SignOutIcon({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true">
      <path d="M6.5 14H3.3A1.3 1.3 0 0 1 2 12.7V3.3A1.3 1.3 0 0 1 3.3 2h3.2" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <path d="m10.5 11.2 3.2-3.2-3.2-3.2M13.7 8H6.2" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

/** User row — the signed-in user's Display Name; opens a menu (Sign out). */
export function PrivasysUserRow({ wide }: SidebarFooterActionOwnerProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  // Resolved at render: the shell sets it before the post-auth dsh boot, so
  // it is ready by the time the sidebar mounts.
  const name = shell().userName?.() || 'User'
  return (
    <div ref={rootRef} className={css.menuRoot}>
      <Menu
        open={open}
        className={css.menuRoot}
        anchor={(
          <FootRow
            wide={wide}
            icon={<IconUserOutline16 size={wide ? 16 : 18} />}
            label={name}
            title={name}
            haspopup="menu"
            expanded={open}
            onClick={() => { setOpen(value => !value) }}
          />
        )}
        items={[{ id: 'sign-out', label: 'Sign out', icon: <SignOutIcon /> }]}
        onSelect={(id) => {
          setOpen(false)
          if (id === 'sign-out') shell().logout?.()
        }}
        onClose={() => { setOpen(false) }}
        side="top"
        align="start"
        portal
        getAnchorRect={() => rootRef.current?.getBoundingClientRect() ?? null}
      />
    </div>
  )
}

/** The column of Privasys rows: the single `sidebar.footer.action` occupant. */
export function PrivasysFootRows({ wide }: SidebarFooterActionOwnerProps) {
  return (
    <div className={wide ? css.column : `${css.column} ${css.rail}`}>
      <PrivasysAttestationRow wide={wide} />
      <PrivasysStorageRow wide={wide} />
      <PrivasysUserRow wide={wide} />
    </div>
  )
}
