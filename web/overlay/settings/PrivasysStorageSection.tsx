/**
 * Settings → Storage: where this holder's conversations and working files are
 * kept, and the one place to connect the Drive or re-send an approval
 * (attested-harness fork). The sidebar shows a row for it only while setup is
 * incomplete (PrivasysLauncher.tsx); a complete setup needs no daily reminder.
 *
 * NOT an option with two acceptable answers. The harness enclave holds no user
 * data: the session root is a tmpfs that dies with the container, so without a
 * Drive nothing is kept at all. The page says SETUP IS INCOMPLETE, never that
 * a preference is unset.
 */
import { useEffect, useSyncExternalStore } from 'react'
import { Button, StateDot } from '@deepseek-ai/dsh-client-ui-primitives'
import { askFolderAgain, refresh, request, snapshot, subscribe, summarise } from './privasys-storage.ts'
import css from './PrivasysSettings.module.css'

export function PrivasysStorageSection() {
  const view = useSyncExternalStore(subscribe, snapshot)
  useEffect(() => { refresh() }, [])
  const s = summarise(view)
  const { ask, busy, waiting, folderAsk } = view

  if (s === undefined) {
    return <div className={css.page}><p className={css.muted}>Checking where your conversations are kept…</p></div>
  }

  return (
    <div className={css.page}>
      <h2 className={css.title}>Where your conversations are kept</h2>
      <div className={css.status}><StateDot state={s.dot} size={10} /><span>{s.word}</span></div>

      {s.persistent
        ? (
          <>
            <p>
              Your conversations are written to <strong>{s.folder}</strong> in your own
              Drive, under your own keys, as they happen: that is your memory, and
              other sessions and agents can draw on it. You can withdraw the Drive
              access at any time in Drive.
            </p>
            {s.stale
              ? (
                <>
                  <p className={css.warn}>
                    This harness now asks for <strong>{(s.missing.length > 0 ? s.missing : view.state?.permissions ?? []).join(', ')}</strong> on
                    that folder (so it can remove sessions you delete and tidy old copies).
                    What you approved earlier keeps working until you answer; approve the
                    updated access on your device to enable it.
                  </p>
                  <div className={css.actions}>
                    <Button variant="primary" size="sm" disabled={busy || waiting} onClick={() => { request(false) }}>
                      {busy ? 'Preparing…' : waiting ? 'Sent to your device…' : ask?.nonce ? 'Send it again' : 'Approve the updated access'}
                    </Button>
                  </div>
                </>
              )
              : null}
          </>
        )
        : (
          <>
            <p>
              Your conversations are not yet a memory you can use elsewhere. Connect
              your Drive and they are written under <strong>{s.folder}</strong> in
              your own Drive, under your own keys, where this harness can reach that
              one folder and nothing else, and where other sessions and agents can
              draw on them. You can withdraw it at any time in Drive.
            </p>
            {s.withdrawn
              ? (
                <p className={css.warn}>
                  Your Drive is refusing this harness: the access was withdrawn there, or
                  has expired. Nothing has been saved since. Connect your Drive again to
                  approve it afresh.
                </p>
              )
              : null}
            {s.declined
              ? <p className={css.muted}>You declined this earlier, so you are not being asked again.</p>
              : null}
            <div className={css.actions}>
              <Button variant="primary" size="sm" disabled={busy || waiting} onClick={() => { request(s.declined || s.withdrawn) }}>
                {busy ? 'Preparing…' : waiting ? 'Sent to your device…' : ask?.nonce ? 'Send it again' : s.withdrawn ? 'Connect again' : s.declined ? 'Ask me again' : 'Connect my Drive'}
              </Button>
            </div>
          </>
        )}

      {ask?.nonce && (!s.persistent || s.stale)
        ? (
          <p className={css.muted}>
            Your wallet verifies this enclave itself and shows you exactly what is
            being asked for. Nothing to do here: this page updates the moment you
            answer on your device.
          </p>
        )
        : null}
      {ask?.status === 'declined' ? <p className={css.muted}>Still declined.</p> : null}

      <h3 className={css.subtitle}>Your working files</h3>
      <p>
        Your agents, skills and working trees are in your own folder here, under
        your own key and the approval you gave on your device. If your wallet no
        longer lists it, ask your device again: the folder stays as it is.
      </p>
      <div className={css.actions}>
        <Button variant="outline" size="sm" disabled={busy} onClick={askFolderAgain}>
          {folderAsk === 'sent' ? 'Sent to your device…' : 'Ask my device again'}
        </Button>
      </div>
      {folderAsk === 'failed' ? <p className={css.muted}>That could not be sent; try again in a moment.</p> : null}
      {folderAsk === 'granted' ? <p className={css.muted}>Your device already has this approval.</p> : null}
    </div>
  )
}
