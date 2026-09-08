/**
 * Privasys occupants for the browser brand + sidebar-foot slots
 * (attested-harness fork). Overrides
 * `packages/client/ui-brand-official/src/client/index.ts`: keeps upstream's
 * brand-slot registrations verbatim (OfficialBrandMark/Name now render the
 * Privasys art via the FishLogo/BrandWordmark overrides in ui-primitives) and
 * ADDS one `sidebar.footer.action` occupant — a column of three rows,
 * Attestation / Sessions / User — at the sidebar foot above Settings, per the
 * designed list slot (ui-sidebar contract: "Optional actions beside Settings
 * at the sidebar foot"; each occupant owns its geometry). One occupant rather
 * than three: dsh lays occupants out in a row, and a column is our layout to
 * own, not the sidebar's to be overridden. No sidebar source is edited.
 */
import type { Context as ClientContext } from '@deepseek-ai/cordis'
import type {} from '@deepseek-ai/dsh-client-ui-renderer/client'
import type {} from '@deepseek-ai/dsh-client-ui-sidebar/client'
import { OfficialBrandMark, OfficialBrandName } from './Brand.tsx'
import { PrivasysFootRows } from './PrivasysRows.tsx'

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
          { name: 'sidebar.footer.action', id: 'privasys-foot' },
          PrivasysFootRows,
        )
      })))
}
