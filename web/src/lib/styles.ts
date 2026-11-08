export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(' ')
}

/** Keyboard focus: a 2px ink ring outside the control. */
export const focusRing = 'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink'

/** A page's padding: 16px on phones, 32px from sm, the full 56px from lg. */
export const pagePadding = 'px-4 pt-6 pb-10 sm:px-8 lg:px-14 lg:pt-9 lg:pb-14'

export type ButtonVariant = 'primary' | 'secondary' | 'danger'
export type ButtonSize = 'md' | 'sm'

const sizes: Record<ButtonSize, string> = {
  md: 'px-[18px] py-[9px] text-body leading-[1.2]',
  sm: 'px-3 py-[5px] text-small leading-[1.2]',
}

const variants: Record<ButtonVariant, string> = {
  primary:
    'border border-ink bg-ink text-paper hover:border-ink-2 hover:bg-ink-2 disabled:border-rule disabled:bg-sunken disabled:text-ink-3',
  secondary: 'border border-ink text-ink hover:bg-sunken disabled:border-rule disabled:bg-transparent disabled:text-ink-3',
  danger: 'border border-danger text-danger hover:bg-danger-bg disabled:border-rule disabled:bg-transparent disabled:text-ink-3',
}

export function buttonClass(variant: ButtonVariant = 'primary', size: ButtonSize = 'md', extra?: string): string {
  return cx(
    'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-sm font-medium disabled:cursor-not-allowed',
    sizes[size],
    variants[variant],
    focusRing,
    extra,
  )
}

export type ActionTone = 'ink' | 'danger'

/**
 * A text action: medium weight with a 1px rule underneath, thicker on hover. An invisible strip above and below
 * makes the touch target 26px tall without moving the rule. A caller that positions the action keeps its position.
 */
export function actionClass(tone: ActionTone = 'ink', size: 'md' | 'sm' = 'md', extra?: string): string {
  const positioned = /(^|\s)(absolute|fixed|sticky|relative)(\s|$)/.test(extra ?? '')
  return cx(
    'inline-flex whitespace-nowrap border-b pb-px font-medium leading-tight hover:border-b-2 hover:pb-0',
    'after:absolute after:inset-x-0 after:-inset-y-1',
    !positioned && 'relative',
    tone === 'danger' ? 'border-danger text-danger' : 'border-ink text-ink',
    size === 'sm' ? 'text-mono' : 'text-small',
    focusRing,
    extra,
  )
}
