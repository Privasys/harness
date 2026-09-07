/**
 * Privasys occupants for the browser brand + sidebar-foot slots
 * (attested-harness fork). Overrides
 * `packages/client/ui-brand-official/src/client/index.ts`: keeps upstream's
 * brand-slot registrations verbatim (OfficialBrandMark/Name now render the
 * Privasys art via the FishLogo/BrandWordmark overrides in ui-primitives) and
 * ADDS two `sidebar.footer.action` rows — Attestation ("Verified") and User
 * (Sign out) — at the sidebar foot next to Settings, per the designed list
 * slot (ui-sidebar contract: "Optional actions beside Settings at the sidebar
 * foot"). No sidebar source is edited.
 */
import type { Context as ClientContext } from '@deepseek-ai/cordis'
import type {} from '@deepseek-ai/dsh-client-ui-renderer/client'
import type {} from '@deepseek-ai/dsh-client-ui-sidebar/client'
import { OfficialBrandMark, OfficialBrandName } from './Brand.tsx'
import { PrivasysUserRow } from './PrivasysRows.tsx'
import { PrivasysAttestationRow } from './PrivasysAttestation.tsx'
import { PrivasysStorageRow } from './PrivasysStorage.tsx'

/** Required service: the UI slot registry. */
export const inject = ['slots']

/**
 * Fill the sidebar brand slots and the Privasys foot rows as one
 * declaration-aware registration set.
 * @param ctx - Client root context.
 */
export function apply(ctx: ClientContext): void {
  if (process.env.DSH_CLIENT_BUILD_PROFILE !== 'official') return
  ctx.slots.inject('sidebar.brand.mark', () =>
    ctx.slots.inject('sidebar.brand.name', () =>
      ctx.slots.inject('sidebar.footer.action', function* () {
        yield ctx.slots.register({ name: 'sidebar.brand.mark' }, OfficialBrandMark)
        yield ctx.slots.register({ name: 'sidebar.brand.name' }, OfficialBrandName)
        yield ctx.slots.register(
          { name: 'sidebar.footer.action', id: 'privasys-attestation' },
          PrivasysAttestationRow,
        )
        yield ctx.slots.register(
          { name: 'sidebar.footer.action', id: 'privasys-storage' },
          PrivasysStorageRow,
        )
        yield ctx.slots.register(
          { name: 'sidebar.footer.action', id: 'privasys-user' },
          PrivasysUserRow,
        )
      })))
}
