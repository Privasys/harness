/**
 * The Settings panel's own actions, bound once by this package's index.ts
 * when it creates the shell store (attested-harness fork). The launcher seat
 * receives only `openSettings`; the sidebar storage row opens the panel on
 * its Storage page, so it needs `openSection` too.
 */

interface ShellActions {
  openSection: (id: string) => void
}

let actions: ShellActions | undefined

export function bindPrivasysSettings(a: ShellActions): void {
  actions = a
}

export function openSettingsSection(id: string): void {
  actions?.openSection(id)
}
