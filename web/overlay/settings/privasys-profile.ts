/**
 * What the assistant calls the user, and their standing instructions
 * (attested-harness fork): one store for the page, read by the sidebar's
 * user row and the user panel's "Your name" and "Your instructions" pages,
 * and written from those pages.
 *
 * The setting is the user's own (profile.json in their Drive folder, served
 * by the proxy, profile.go). The sign-in token carries the name the wallet
 * disclosed, but only on a sign-in the wallet took part in, so it seeds the
 * setting once and is the fallback while the setting is empty; a renewed
 * session's token has no name, which is why the row said "Account".
 */

interface PrivasysShellHooks {
  /** The `name` claim of the sign-in token, when the wallet disclosed it. */
  userName?: () => string | undefined
}

function tokenName(): string {
  const hooks = (globalThis as { __PRIVASYS_SHELL__?: PrivasysShellHooks }).__PRIVASYS_SHELL__
  return hooks?.userName?.()?.trim() ?? ''
}

function pvFetch(input: string, init?: RequestInit): Promise<Response> {
  const t = (globalThis as { __DSH_TRANSPORT__?: { fetch?: typeof fetch } }).__DSH_TRANSPORT__
  return t?.fetch !== undefined ? t.fetch(input, init) : fetch(input, init)
}

export interface ProfileView {
  /** The name; "" while unset or not yet read. */
  name: string
  /** The standing instructions; "" while unset or not yet read. */
  instructions: string
  loaded: boolean
  /** False after a save that could not reach the Drive. */
  persisted: boolean
}

/** What a save changes: only the fields named. */
export interface ProfilePatch {
  name?: string
  instructions?: string
}

interface ProfileWire {
  name?: string
  instructions?: string
  persisted?: boolean
  error?: string
}

let view: ProfileView = { name: '', instructions: '', loaded: false, persisted: true }
const listeners = new Set<() => void>()
let started = false

function set(patch: Partial<ProfileView>): void {
  view = { ...view, ...patch }
  for (const l of listeners) l()
}

function fromWire(d: ProfileWire): Pick<ProfileView, 'name' | 'instructions'> {
  return {
    name: typeof d.name === 'string' ? d.name : '',
    instructions: typeof d.instructions === 'string' ? d.instructions : '',
  }
}

async function load(): Promise<void> {
  try {
    const r = await pvFetch('/privasys/profile')
    if (!r.ok) return
    const d = fromWire(await r.json() as ProfileWire)
    set({ ...d, loaded: true })
    // Seed once from the sign-in: the name the wallet disclosed is the
    // natural first answer, and the user changes it from the panel.
    const seed = tokenName()
    if (d.name === '' && seed !== '') void save({ name: seed })
  } catch { /* the row keeps its fallback */ }
}

export function subscribe(listener: () => void): () => void {
  if (!started) { started = true; void load() }
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function snapshot(): ProfileView {
  return view
}

/** The name to show: the setting, else the sign-in's, else "". */
export function displayName(v: ProfileView): string {
  return v.name !== '' ? v.name : tokenName()
}

/** Save the fields named; resolves to an error sentence, or "" on success. */
export async function save(patch: ProfilePatch): Promise<string> {
  try {
    const r = await pvFetch('/privasys/profile', {
      method: 'PUT',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(patch),
    })
    const d = await r.json() as ProfileWire
    if (!r.ok) return d.error ?? 'The change could not be saved.'
    set({ ...fromWire(d), loaded: true, persisted: d.persisted !== false })
    return ''
  } catch {
    return 'The change could not be saved; try again in a moment.'
  }
}
