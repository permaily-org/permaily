import type { InputHTMLAttributes, ReactNode, Ref } from 'react'
import { cx, focusRing } from '../../lib/styles.ts'

/**
 * A labelled text field with its hint and its error. The error is announced when it appears,
 * and both are read with the field when it takes focus.
 */
export function Field({
  id,
  label,
  tip,
  hint,
  error,
  describedBy,
  inputRef,
  className,
  ...props
}: Omit<InputHTMLAttributes<HTMLInputElement>, 'id'> & {
  id: string
  label: string
  /** An InfoTip beside the label. */
  tip?: ReactNode
  hint?: ReactNode
  error?: string | null
  /** Ids of other text on the page that explains this field. */
  describedBy?: string
  inputRef?: Ref<HTMLInputElement>
}) {
  const hintId = `${id}-hint`
  const errorId = `${id}-error`
  const described = [describedBy, error ? errorId : null, hint ? hintId : null]
    .filter(Boolean)
    .join(' ')
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5">
        <label htmlFor={id} className="text-small text-ink-2">
          {label}
        </label>
        {tip}
      </div>
      <input
        {...props}
        id={id}
        ref={inputRef}
        aria-invalid={error ? true : undefined}
        aria-describedby={described || undefined}
        className={cx(
          'w-full rounded-sm border bg-transparent px-3 py-2.5 text-body text-ink placeholder:text-ink-3',
          error ? 'border-danger' : 'border-ink',
          focusRing,
          className,
        )}
      />
      {/* Always in the page, so a screen reader hears the error the moment it is written. */}
      <p id={errorId} role="alert" className="text-small text-danger empty:hidden">
        {error}
      </p>
      {hint && (
        <p id={hintId} className="text-small text-ink-2">
          {hint}
        </p>
      )}
    </div>
  )
}
