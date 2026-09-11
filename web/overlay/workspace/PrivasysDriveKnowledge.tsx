/**
 * Privasys: per-workspace Drive knowledge (attested-harness fork).
 *
 * A workspace decides what of the user's Drive its sessions may read through
 * the Drive tools: nothing, every folder the user enabled for AI in Drive, or
 * a subset of those folders. The setting is the user's own document on their
 * Drive and is enforced by the harness's egress proxy on every tool call; this
 * dialog only reads and writes it.
 *
 * Reached over a module-level bus (a workspace row's menu, and the workspace
 * creation flow), the same way the session-delete dialog is, so no upstream
 * prop chain has to change.
 */
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { Button, Modal } from '@deepseek-ai/dsh-client-ui-primitives'
import css from './WorkspaceBrowser.module.css'

export interface DriveKnowledgeRequest {
  readonly workspaceId: string
  readonly title: string
}

type Listener = (request: DriveKnowledgeRequest) => void
const listeners = new Set<Listener>()

/** Open the Drive-knowledge dialog for one workspace. */
export function requestDriveKnowledge(workspaceId: string, title: string): void {
  for (const listener of listeners) listener({ workspaceId, title })
}

/** Subscribe to dialog requests; returns the unsubscribe function. */
export function onDriveKnowledgeRequest(listener: Listener): () => void {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

type Mode = 'off' | 'all' | 'selected'

interface AvailableFolder {
  node_id: string
  name: string
  always?: boolean
}

interface KnowledgeState {
  mode: Mode
  folders: string[]
  available?: AvailableFolder[]
  all_scoped?: boolean
  available_error?: string
}

function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

/** The dialog. Mounted once by the workspace browser; opens on a bus request. */
export function PrivasysDriveKnowledgeDialog(): ReactNode {
  const [target, setTarget] = useState<DriveKnowledgeRequest | null>(null)
  const [state, setState] = useState<KnowledgeState | null>(null)
  const [mode, setMode] = useState<Mode>('all')
  const [chosen, setChosen] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => onDriveKnowledgeRequest((request) => {
    setTarget(request)
    setState(null)
    setError(null)
    setNotice(null)
    setBusy(true)
    void pvFetch(`/privasys/workspaces/${encodeURIComponent(request.workspaceId)}/drive-knowledge`)
      .then(async (r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return await r.json() as KnowledgeState
      })
      .then((s) => {
        setState(s)
        setMode(s.mode)
        setChosen(new Set(s.folders ?? []))
        setBusy(false)
      }, (reason: unknown) => {
        setError(reason instanceof Error ? reason.message : String(reason))
        setBusy(false)
      })
  }), [])

  const close = (): void => {
    if (busy) return
    setTarget(null)
  }

  const save = (): void => {
    if (target === null || busy) return
    setBusy(true)
    setError(null)
    const body = mode === 'selected'
      ? { mode, folders: [...chosen] }
      : { mode }
    void pvFetch(`/privasys/workspaces/${encodeURIComponent(target.workspaceId)}/drive-knowledge`, {
      method: 'PUT',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    }).then(async (r) => {
      const j = await r.json() as { error?: string; notice?: string; persisted?: boolean }
      if (!r.ok) throw new Error(j.error ?? `HTTP ${r.status}`)
      setBusy(false)
      if (j.notice !== undefined) {
        setNotice(j.notice)
        return
      }
      setTarget(null)
    }, (reason: unknown) => {
      setBusy(false)
      setError(reason instanceof Error ? reason.message : String(reason))
    })
  }

  const toggle = (id: string): void => {
    setChosen((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const available = state?.available ?? []
  const radio = (value: Mode, label: string, hint: string): ReactNode => (
    <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', margin: '6px 0', cursor: 'pointer' }}>
      <input
        type="radio"
        name="privasys-drive-knowledge"
        value={value}
        checked={mode === value}
        disabled={busy}
        onChange={() => { setMode(value) }}
        style={{ marginTop: 3 }}
      />
      <span>
        <span style={{ display: 'block' }}>{label}</span>
        <span style={{ display: 'block', fontSize: '0.9em', opacity: 0.75 }}>{hint}</span>
      </span>
    </label>
  )

  return (
    <Modal
      open={target !== null}
      onClose={close}
      closeLabel="Close"
      title="Drive knowledge"
      {...target === null ? {} : { description: `What sessions in “${target.title}” may read from your Drive.` }}
      footer={(
        <>
          <Button variant="outline" disabled={busy} onClick={close}>Cancel</Button>
          <Button variant="primary" disabled={busy || state === null} onClick={save}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </>
      )}
    >
      <div>
        {radio('all', 'All folders enabled for AI',
          'Everything you have enabled for AI in your Drive, including Memory. The default.')}
        {radio('selected', 'Selected folders',
          'Only the folders ticked below. Keeps other customers’ or projects’ files out of this workspace.')}
        {radio('off', 'Off',
          'Sessions in this workspace cannot read your Drive at all.')}
        {mode === 'selected' && (
          <div style={{ margin: '8px 0 0 24px' }}>
            {state?.available_error !== undefined && (
              <div className={css.renameError} role="alert">
                Your Drive did not answer, so the folder list is unavailable: {state.available_error}
              </div>
            )}
            {available.length === 0 && state?.available_error === undefined && (
              <p style={{ opacity: 0.75 }}>
                No folder is enabled for AI in your Drive yet. Enable folders there first, then pick them here.
              </p>
            )}
            {available.map(f => (
              <label key={f.node_id} style={{ display: 'flex', gap: 8, alignItems: 'center', margin: '4px 0', cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={chosen.has(f.node_id)}
                  disabled={busy}
                  onChange={() => { toggle(f.node_id) }}
                />
                <span>{f.name}{f.always === true ? ' (always enabled in Drive)' : ''}</span>
              </label>
            ))}
            {state?.all_scoped === true && (
              <p style={{ opacity: 0.75, fontSize: '0.9em' }}>
                Your whole Drive is enabled for AI, so every top-level folder is listed.
              </p>
            )}
          </div>
        )}
        {notice !== null && <div className={css.deleteStatus} role="status">{notice}</div>}
        {error !== null && <div className={css.renameError} role="alert">{error}</div>}
      </div>
    </Modal>
  )
}
