/**
 * The signed-in user's panel (attested-harness fork): the same modal as
 * Settings (SettingsRoot's panel, nav rail and content column, drawn with
 * SettingsRoot.module.css so the two read as one family), with its own pages.
 *
 *   Privacy policy, Terms of service  the brand's own pages (brand.json
 *                                     "legal"), shown in the content column
 *   Sign out                          ends the sealed session in this browser
 *
 * The pages to come (how the assistant addresses you, your language, your
 * default instructions) are more entries in PAGES.
 *
 * The legal pages are the brand's public web pages, framed as they are: the
 * browser fetches them from the brand's site, never through the enclave, and
 * only once the page is opened.
 */
import { useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import clsx from 'clsx'
import {
  Button, IconCloseOutlineRegular, IconDeliverDocMedium, IconShieldOutlineMedium, useModalLayer,
} from '@deepseek-ai/dsh-client-ui-primitives'
import { PRIVASYS_LEGAL } from './privasys-legal.ts'
import root from './SettingsRoot.module.css'
import css from './PrivasysSettings.module.css'

interface PrivasysShellHooks {
  logout?: () => void
  /** The signed-in user's Display Name (the `profile`-scope `name` claim). */
  userName?: () => string | undefined
}

export function shellHooks(): PrivasysShellHooks {
  return (globalThis as { __PRIVASYS_SHELL__?: PrivasysShellHooks }).__PRIVASYS_SHELL__ ?? {}
}

export function SignOutIcon({ size = 16, className }: { size?: number; className?: string | undefined }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true" className={className}>
      <path d="M6.5 14H3.3A1.3 1.3 0 0 1 2 12.7V3.3A1.3 1.3 0 0 1 3.3 2h3.2" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
      <path d="m10.5 11.2 3.2-3.2-3.2-3.2M13.7 8H6.2" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

type PageId = 'privacy' | 'terms' | 'sign-out'

interface Page {
  id: PageId
  label: string
  icon: ReactNode
}

const PAGES: readonly Page[] = [
  { id: 'privacy', label: 'Privacy policy', icon: <IconShieldOutlineMedium className={root.navIcon} size={16} /> },
  { id: 'terms', label: 'Terms of service', icon: <IconDeliverDocMedium className={root.navIcon} size={16} /> },
  { id: 'sign-out', label: 'Sign out', icon: <SignOutIcon className={root.navIcon} /> },
]

function LegalPage({ href, title }: { href: string; title: string }) {
  return (
    <div className={css.legal}>
      <a className={css.external} href={href} target="_blank" rel="noopener noreferrer">Open in a new tab</a>
      <iframe
        className={css.frame}
        src={href}
        title={title}
        referrerPolicy="no-referrer"
        sandbox="allow-scripts allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      />
    </div>
  )
}

function SignOutPage({ name }: { name: string }) {
  const [busy, setBusy] = useState(false)
  return (
    <div className={css.page}>
      <h2 className={css.title}>Sign out</h2>
      <p>
        You are signed in as <strong>{name}</strong>. Signing out ends your session
        in this browser. Your conversations and working files stay where they are
        kept, and you sign in again with your wallet.
      </p>
      <div className={css.actions}>
        <Button variant="primary" size="sm" disabled={busy} onClick={() => { setBusy(true); shellHooks().logout?.() }}>
          {busy ? 'Signing out…' : 'Sign out'}
        </Button>
      </div>
    </div>
  )
}

/**
 * Body-portaled modal, the Settings panel's twin: a mask click, the close
 * button and Escape (useModalLayer) close it.
 */
export function PrivasysUserPanel({ name, onClose }: { name: string; onClose: () => void }) {
  const [active, setActive] = useState<PageId>('privacy')
  const titleId = useId()
  const panel = useRef<HTMLDivElement>(null)
  useModalLayer(panel, true, onClose)

  return createPortal((
    <div className={root.overlay} role="presentation">
      <div className={root.mask} aria-hidden="true" onClick={onClose} />
      <div ref={panel} tabIndex={-1} className={root.panel} role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <nav className={root.nav}>
          <div className={root.navTitle} id={titleId} tabIndex={-1}>{name}</div>
          <div className={root.navList}>
            {PAGES.map(page => (
              <button
                key={page.id}
                type="button"
                className={clsx(root.navCell, page.id === active && root.active)}
                aria-current={page.id === active ? 'true' : undefined}
                data-modal-autofocus={page.id === active ? '' : undefined}
                onClick={() => { setActive(page.id) }}
              >
                {page.icon}
                <span className={root.navLabel}>{page.label}</span>
              </button>
            ))}
          </div>
        </nav>
        <div className={root.content}>
          <div className={root.header}>
            <div className={root.actions} />
            <button type="button" className={root.close} onClick={onClose}>
              <IconCloseOutlineRegular size={14} />
              <span className={root.hiddenLabel}>Close</span>
            </button>
          </div>
          <div className={clsx(root.options, active !== 'sign-out' && css.optionsFill)}>
            {active === 'privacy' && <LegalPage href={PRIVASYS_LEGAL.privacy} title="Privacy policy" />}
            {active === 'terms' && <LegalPage href={PRIVASYS_LEGAL.terms} title="Terms of service" />}
            {active === 'sign-out' && <SignOutPage name={name} />}
          </div>
        </div>
      </div>
    </div>
  ), document.body)
}
