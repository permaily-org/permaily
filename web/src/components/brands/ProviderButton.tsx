import type { ComponentProps } from 'react'
import type { Provider } from '../../lib/api.ts'
import { providerAction } from '../../lib/providers.ts'
import { buttonVariants, cx, focusRing } from '../../lib/styles.ts'
import { ProviderMark } from './marks.tsx'
import './brands.css'

/**
 * How each provider's button looks. GitHub's and Microsoft's rules leave the button to us: the design system's
 * secondary button. Google's and Facebook's fix its colours, kept in `brands.css`.
 */
const looks: Record<Provider, string> = {
  google: 'provider-google border',
  github: buttonVariants.secondary,
  meta: 'provider-facebook border',
  microsoft: buttonVariants.secondary,
}

/**
 * A provider's sign-in button: its mark, then its words, which are the button's name. The shape is the design
 * system's (corners, type, focus ring), 40px tall with 12px on each side and 10px between the mark and the
 * words, which is the padding Google fixes and the clear space Meta, GitHub and Microsoft ask for.
 */
export function ProviderButton({
  provider,
  className,
  ...props
}: Omit<ComponentProps<'button'>, 'children'> & { provider: Provider }) {
  const ownColours = provider === 'google' || provider === 'meta'
  return (
    <button
      type="button"
      {...props}
      className={cx(
        'group inline-flex h-10 items-center justify-center gap-2.5 whitespace-nowrap rounded-sm px-3 font-medium text-body leading-5 disabled:cursor-not-allowed',
        looks[provider],
        focusRing,
        className,
      )}
    >
      <ProviderMark provider={provider} />
      {/* The mark keeps its colours while the button waits; the words fade, as the design system's do. */}
      <span className={cx(ownColours && 'group-disabled:opacity-60')}>
        {providerAction(provider)}
      </span>
    </button>
  )
}
