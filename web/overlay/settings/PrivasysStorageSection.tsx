/**
 * Settings → Storage: where this holder's conversations and working files are
 * kept (attested-harness fork). One line per state, and a button only when
 * the person is the one who can mend it: everything the harness can do by
 * itself (a re-arm after a roll, a push still on its way) it does before
 * asking. The sidebar shows a row for it only while something needs them
 * (PrivasysLauncher.tsx).
 *
 * NOT an option with two acceptable answers. The harness enclave holds no user
 * data: the session root is a tmpfs that dies with the container, so without a
 * Drive nothing is kept at all. The page says SETUP IS INCOMPLETE, never that
 * a preference is unset.
 */
import { useEffect, useSyncExternalStore } from 'react'
import { Button, StateDot } from '@deepseek-ai/dsh-client-ui-primitives'
import { askFolderAgain, refresh, request, snapshot, subscribe, summarise, workingFiles } from './privasys-storage.ts'
import css from './PrivasysSettings.module.css'

export function PrivasysStorageSection() {
  const view = useSyncExternalStore(subscribe, snapshot)
  useEffect(() => { refresh() }, [])
  const s = summarise(view)
  const { ask, busy, waiting, folderAsk } = view

  if (s === undefined) {
    return <div className={css.page}><p className={css.muted}>Checking where your conversations are kept…</p></div>
  }
  const wf = workingFiles(view)

  // The Drive line, and the one action it may need.
  const driveAction = s.persistent && !s.stale
    ? undefined
    : {
      label: busy ? 'Preparing…'
        : waiting ? 'Sent to your device…'
          : ask?.nonce ? 'Send it again'
            : s.stale ? 'Approve the updated access'
              : s.withdrawn ? 'Connect again'
                : s.declined ? 'Ask me again' : 'Connect my Drive',
      run: () => { request(s.declined || s.withdrawn) },
    }
  const driveLine = s.stale
    ? `Your conversations are in ${s.folder} in your Drive. This harness now asks for ${(s.missing.length > 0 ? s.missing : view.state?.permissions ?? []).join(', ')} on that folder; approve the updated access on your device.`
    : s.persistent ? `Your conversations are written to ${s.folder} in your own Drive, under your own keys, as they happen.`
      : s.withdrawn ? 'Your Drive is refusing this harness: the access was withdrawn there, or has expired. Nothing has been saved since.'
        : s.declined ? 'You declined this earlier, so you are not being asked again.'
          : 'Your conversations are not yet kept anywhere: connect your Drive and they are written to it under your own keys.'

  return (
    <div className={css.page}>
      <h2 className={css.title}>Your conversations</h2>
      <div className={css.status}><StateDot state={s.dot} size={10} /><span>{s.word}</span></div>
      <p>{driveLine}</p>
      {driveAction !== undefined && (
        <div className={css.actions}>
          <Button variant="primary" size="sm" disabled={busy || waiting} onClick={driveAction.run}>{driveAction.label}</Button>
        </div>
      )}
      {ask?.nonce && driveAction !== undefined && (
        <p className={css.muted}>
          Your wallet verifies this enclave itself and shows you what is asked for. This
          page updates the moment you answer on your device.
        </p>
      )}
      {ask?.status === 'declined' ? <p className={css.muted}>Still declined.</p> : null}

      <h3 className={css.subtitle}>Your working files</h3>
      <div className={css.status}><StateDot state={wf.dot} size={10} /><span>{wf.word}</span></div>
      <p>{wf.line}</p>
      {wf.ask && (
        <div className={css.actions}>
          <Button variant={wf.state === 'locked' ? 'primary' : 'outline'} size="sm" disabled={busy || folderAsk === 'sent'} onClick={askFolderAgain}>
            {folderAsk === 'sent' ? 'Sent to your wallet…' : wf.state === 'locked' ? 'Unlock with the Privasys wallet' : 'Approve with the Privasys wallet'}
          </Button>
        </div>
      )}
      {folderAsk === 'failed' ? <p className={css.muted}>That could not be sent to your wallet; try again in a moment.</p> : null}
      {folderAsk === 'granted' ? <p className={css.muted}>Your device already has this approval; the folder opens at the next start.</p> : null}

      <details className={css.section}>
        <summary className={css.muted}>What are these two?</summary>
        <p className={css.muted}>
          Your working files (your agents, skills and working trees) are a folder
          here, in this enclave, under a key only your wallet holds; the harness keeps
          a wrapped copy so your agents can run while you are away. Your conversations
          are also written to one folder in your Drive, under your own keys, where other
          sessions and agents can draw on them. You can withdraw either in your wallet
          or in Drive at any time.
        </p>
      </details>
    </div>
  )
}
