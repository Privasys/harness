/**
 * Privasys: the "Delete session" row action (attested-harness fork).
 *
 * dsh's row menu reaches the browser root through a chain of props
 * (Rows → group rows → search rows → WorkspaceBrowser). Adding one more
 * callback to that chain means touching every hop, and each hop is an
 * anchor the next upstream release can move. So the row publishes the
 * request on this tiny module-level bus and the browser root, which owns
 * the confirmation dialog and the Host command, subscribes once. Same
 * page, same module instance: nothing crosses a boundary.
 */
export interface SessionDeleteRequest {
  readonly sessionId: string
  readonly title: string
}

type Listener = (request: SessionDeleteRequest) => void

const listeners = new Set<Listener>()

/** Ask the browser root to confirm and delete one session. */
export function requestSessionDelete(sessionId: string, title: string): void {
  for (const listener of listeners) listener({ sessionId, title })
}

/**
 * Subscribe to delete requests.
 * @returns the unsubscribe function (effect-cleanup shaped).
 */
export function onSessionDeleteRequest(listener: Listener): () => void {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}
