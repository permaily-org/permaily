import { cx, focusRing } from '../../lib/styles.ts'

/** Filled danger button for the final step of a destructive action. */
export const solidDanger = cx(
  'inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-sm border border-danger bg-danger px-[18px] py-2.5 text-body font-medium text-on-danger hover:bg-danger/90',
  focusRing,
)

/** Outlined button inside an attention notice. */
export const attentionButton = cx(
  'inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-sm border border-attention px-3 py-1.5 text-small font-medium text-attention hover:bg-paper',
  focusRing,
)
