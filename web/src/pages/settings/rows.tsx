import type { ReactNode } from 'react'
import { cx, focusRing } from '../../lib/styles.ts'

export function SettingRow({
  label,
  labelFor,
  children,
  action,
}: {
  label: string
  /** The id of the field this row is for, when it holds one: the label then names it. */
  labelFor?: string
  children: ReactNode
  action?: ReactNode
}) {
  const Label = labelFor ? 'label' : 'div'
  return (
    <div className="flex min-h-[39px] flex-col items-start gap-0.5 border-b border-rule py-2 sm:flex-row sm:items-center sm:gap-4">
      <Label htmlFor={labelFor} className="shrink-0 text-small text-ink-2 sm:w-[116px]">
        {label}
      </Label>
      <div className="flex w-full min-w-0 flex-1 items-center justify-between gap-3">
        <div className="min-w-0 flex-1 text-body">{children}</div>
        {action}
      </div>
    </div>
  )
}

/** The text field of a setting that is changed in place. */
export const field = cx(
  'min-w-0 flex-1 basis-32 rounded-sm border border-ink bg-paper px-2 py-px text-small text-ink max-md:min-h-8',
  focusRing,
)
