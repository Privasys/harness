/**
 * Privasys: the confirmation for "Delete session" (attested-harness fork).
 *
 * The menu entry publishes on the bus (PrivasysSessionDelete.ts) and closes;
 * this dialog sits at the browser root, where it survives the menu closing
 * under it. It confirms, the Host deletes, and the removal event drops the row.
 */
import { useEffect, useState } from 'react'
import { Button, Modal } from '@deepseek-ai/dsh-client-ui-primitives'
import type { SessionId } from '@deepseek-ai/dsh-session/types'
import type { WorkspaceBrowserProps } from '../contract/slots.ts'
import { onSessionDeleteRequest } from './PrivasysSessionDelete.ts'
import css from './WorkspaceBrowser.module.css'

/**
 * @param props - the Host's delete command and the workspace locale seat.
 * @returns the dialog, closed until a request arrives.
 */
export function PrivasysSessionDeleteDialog({ deleteSession, t }: {
  deleteSession: WorkspaceBrowserProps['deleteSession'] | undefined
  t: WorkspaceBrowserProps['t']
}) {
  const [target, setTarget] = useState<{ sessionId: SessionId, title: string } | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => onSessionDeleteRequest((request) => {
    setTarget({ sessionId: request.sessionId as SessionId, title: request.title })
    setError(null)
  }), [])
  const close = () => {
    if (deleting) return
    setTarget(null)
    setError(null)
  }
  const confirm = () => {
    if (deleting || target === null || deleteSession === undefined) return
    setDeleting(true)
    setError(null)
    deleteSession(target.sessionId).then(() => {
      setDeleting(false)
      setTarget(null)
    }).catch((reason: unknown) => {
      setDeleting(false)
      setError(reason instanceof Error ? reason.message : String(reason))
    })
  }
  return (
    <Modal
      open={target !== null}
      onClose={close}
      closeLabel={t('close')}
      title={t('delete.session')}
      {...target === null ? {} : { description: t('delete.session.desc', { name: target.title }) }}
      footer={(
        <>
          <Button variant="outline" disabled={deleting} onClick={close}>{t('cancel')}</Button>
          <Button variant="outline" className={css.deleteAction} disabled={deleting} onClick={confirm}>
            {t('delete.session')}
          </Button>
        </>
      )}
    >
      {deleting && <div className={css.deleteStatus} role="status">{t('delete.session.pending')}</div>}
      {error !== null && <div className={css.renameError} role="alert">{error}</div>}
    </Modal>
  )
}
