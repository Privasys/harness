/**
 * "Privasys AI" wordmark in place of the DeepSeek wordmark svg
 * (attested-harness fork). Overrides
 * `packages/client/ui-primitives/src/BrandWordmark.tsx` keeping the exact
 * upstream interface (BrandWordmarkProps, size = height in px, includeMark).
 * Upstream is a hardcoded svg of the DeepSeek name + whale + HARNESS badge;
 * ours renders the Privasys mark plus plain text, which tracks the UI font.
 */
import type { IconProps } from './icons/props.ts'
import { FishLogo } from './FishLogo.tsx'

/** Display options for the official brand wordmark. */
export interface BrandWordmarkProps extends IconProps {
  /** Whether to include the leading Privasys mark; defaults to true. */
  includeMark?: boolean | undefined
}

/**
 * Render the full brand wordmark.
 * @param props.size - height in px (default 24; width follows the content).
 * @param props.className - extra class for layout placement.
 * @param props.includeMark - whether to include the leading Privasys mark.
 * @returns the wordmark (aria-hidden decorative brand art).
 */
export function BrandWordmark({ size = 24, className, includeMark = true }: BrandWordmarkProps) {
  return (
    <span
      className={className}
      aria-hidden="true"
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: Math.round(size / 3),
        height: size,
        whiteSpace: 'nowrap',
      }}
    >
      {includeMark ? <FishLogo size={size} /> : null}
      <span
        style={{
          fontSize: Math.round(size * 0.66),
          fontWeight: 600,
          letterSpacing: '-0.01em',
          lineHeight: 1,
        }}
      >
        Privasys AI
      </span>
    </span>
  )
}
