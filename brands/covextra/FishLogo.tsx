/**
 * Covextra mark in place of the DeepSeek whale (brand "covextra").
 *
 * Same exports and shapes as the Privasys override in overlay/brand, which
 * the build replaces with this file when HARNESS_BRAND=covextra. The mark is
 * the X of the Covextra logo: an ink stroke and a teal stroke. Ink follows
 * the UI's text colour so it reads on either theme, and the teal takes the
 * brand's reversed shade when dsh sets a dark colour scheme.
 */
import type { IconProps } from './icons/props.ts'

/** Native viewBox of {@link FISH_LOGO_PATH} (width and height in user units). */
export const FISH_LOGO_VIEWBOX = { width: 64, height: 64 }

const INK = 'M11.24 21.24h11.78L48.76 54.00h-11.78z'
const ACCENT = 'M11.24 54.00h11.78L56.54 11.34h-11.78z'

/** The mark as one silhouette path, for consumers that draw it monochrome. */
export const FISH_LOGO_PATH = `${INK} ${ACCENT}`

/**
 * Render the Covextra mark (brand colours).
 * @param props.size - width in px (default 24; square).
 * @param props.className - extra class for layout placement.
 * @returns the mark svg (aria-hidden; pair with the wordmark for accessibility).
 */
export function FishLogo({ size = 24, className }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      className={className}
      viewBox={`0 0 ${FISH_LOGO_VIEWBOX.width} ${FISH_LOGO_VIEWBOX.height}`}
      fill="none"
      aria-hidden="true"
    >
      <path d={INK} fill="currentColor" />
      <path d={ACCENT} style={{ fill: 'light-dark(#00757D, #35D9CE)' }} />
    </svg>
  )
}
