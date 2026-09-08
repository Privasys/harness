/**
 * Same-origin fetch over the sealed session (attested-harness fork).
 *
 * The proxy's per-user endpoints (/privasys/sampling, /privasys/spend, …)
 * file everything under the relay-asserted X-Privasys-Sub, which the relay
 * sets only on sealed requests and strips from everything else. The vanilla
 * auth shell installs the sealed transport as `window.__DSH_TRANSPORT__`;
 * off platform there is no sealed session and the raw fetch is right.
 */
export function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

/** GET a JSON document; `undefined` on any failure. */
export async function pvGetJson<T>(path: string): Promise<T | undefined> {
  try {
    const r = await pvFetch(path)
    if (!r.ok) return undefined
    return await r.json() as T
  } catch {
    return undefined
  }
}

/** Send a JSON body; throws with the server's error text on a non-2xx. */
export async function pvSendJson<T>(method: 'PUT' | 'POST' | 'DELETE', path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method }
  if (body !== undefined) {
    init.headers = { 'content-type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  const r = await pvFetch(path, init)
  if (!r.ok) {
    let message = `HTTP ${r.status}`
    try {
      const j = await r.json() as { error?: string }
      if (typeof j.error === 'string') message = j.error
    } catch {
      // status alone
    }
    throw new Error(message)
  }
  return await r.json() as T
}
