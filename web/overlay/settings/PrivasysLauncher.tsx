/**
 * The Settings seat at the sidebar foot (attested-harness fork), occupying
 * `settings.launcher`: the seat dsh designed for an account launcher, left
 * free on the web (dsh's own account launcher is desktop-only). It renders, as
 * one column:
 *
 *   Storage   only while setup is incomplete: opens Settings on Storage
 *   Settings  opens the Settings panel, as dsh's own trigger does
 *   <name>    the signed-in user: opens the user panel (PrivasysUserPanel)
 *
 * Every row is PrivasysFootRow, the control the Attestation row above is
 * built from, so the foot reads as one family.
 */
import { useState, useSyncExternalStore } from 'react'
import {
  IconFolderOpenOutlineRegular, IconSettingsOutlineMedium, IconUserOutlineRegular, StateDot,
} from '@deepseek-ai/dsh-client-ui-primitives'
import type { SettingsLauncherOwnerProps } from '@deepseek-ai/dsh-client-ui-settings/client'
import { FootRow } from './PrivasysFootRow.tsx'
import { PrivasysUserPanel } from './PrivasysUserPanel.tsx'
import { displayName, snapshot as profileSnapshot, subscribe as profileSubscribe } from './privasys-profile.ts'
import { openSettingsSection } from './privasys-settings-shell.ts'
import { snapshot, subscribe, summarise } from './privasys-storage.ts'
import foot from './PrivasysFoot.module.css'
import css from './PrivasysSettings.module.css'

function StorageAlertRow({ wide }: { wide: boolean }) {
  const s = summarise(useSyncExternalStore(subscribe, snapshot))
  if (s === undefined || s.healthy) return null
  // The attribute lets the stylesheet place dsh's connection pill on the
  // Settings row below this one.
  return (
    <div data-privasys-storage-alert="">
    <FootRow
      wide={wide}
      icon={<IconFolderOpenOutlineRegular size={wide ? 16 : 18} />}
      label="Storage"
      status={<><StateDot state={s.dot} size={10} /><span>{s.word}</span></>}
      title="Connect your Drive to keep your conversations"
      ariaLabel={`Storage: ${s.word}`}
      haspopup="dialog"
      onClick={() => { openSettingsSection('storage') }}
    />
    </div>
  )
}

export function PrivasysLauncher({ wide, settingsOpen, openSettings }: SettingsLauncherOwnerProps) {
  const [userOpen, setUserOpen] = useState(false)
  // What the assistant calls the user (their setting, else the sign-in's).
  const name = displayName(useSyncExternalStore(profileSubscribe, profileSnapshot)) || 'Account'
  return (
    <div className={wide ? `${foot.column} ${css.launcher}` : `${foot.column} ${foot.rail} ${css.launcher}`}>
      <StorageAlertRow wide={wide} />
      <FootRow
        wide={wide}
        icon={<IconSettingsOutlineMedium size={wide ? 16 : 18} />}
        label="Settings"
        title="Settings"
        haspopup="dialog"
        expanded={settingsOpen}
        onClick={openSettings}
      />
      <FootRow
        wide={wide}
        icon={<IconUserOutlineRegular size={wide ? 16 : 18} />}
        label={name}
        title={name}
        haspopup="dialog"
        expanded={userOpen}
        onClick={() => { setUserOpen(true) }}
      />
      {userOpen && <PrivasysUserPanel name={name} onClose={() => { setUserOpen(false) }} />}
    </div>
  )
}
