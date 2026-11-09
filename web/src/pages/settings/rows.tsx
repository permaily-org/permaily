import { type ReactNode, useState } from 'react'
import { Switch, TextAction } from '../../components/ui/index.ts'
import { actionClass, cx, focusRing } from '../../lib/styles.ts'

export function SettingRow({
  label,
  children,
  action,
}: {
  label: string
  children: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex min-h-[39px] flex-col items-start gap-0.5 border-b border-rule py-2 sm:flex-row sm:items-center sm:gap-4">
      <div className="shrink-0 text-small text-ink-2 sm:w-[116px]">{label}</div>
      <div className="flex w-full min-w-0 flex-1 items-center justify-between gap-3">
        <div className="min-w-0 flex-1 text-body">{children}</div>
        {action}
      </div>
    </div>
  )
}

const field = cx(
  'min-w-0 flex-1 basis-32 rounded-sm border border-ink bg-paper px-2 py-px text-small text-ink max-md:min-h-8',
  focusRing,
)

/** A setting whose value can be changed in place, with a text field or a list of choices. */
export function EditableRow({
  label,
  value,
  onSave,
  actionLabel = 'Change',
  options,
  type = 'text',
  mono,
}: {
  label: string
  value: string
  onSave: (value: string) => void
  actionLabel?: string
  options?: string[]
  type?: 'text' | 'email'
  mono?: boolean
}) {
  const [draft, setDraft] = useState<string | null>(null)

  if (draft === null) {
    return (
      <SettingRow
        label={label}
        action={<TextAction onClick={() => setDraft(value)}>{actionLabel}</TextAction>}
      >
        <span className={cx(mono && 'font-mono text-small')}>{value}</span>
      </SettingRow>
    )
  }

  return (
    <SettingRow label={label}>
      <form
        className="flex w-full flex-wrap items-center gap-x-3 gap-y-2"
        onSubmit={(event) => {
          event.preventDefault()
          if (draft.trim()) onSave(draft.trim())
          setDraft(null)
        }}
      >
        {options ? (
          <select
            aria-label={label}
            autoFocus
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            className={field}
          >
            {options.map((option) => (
              <option key={option}>{option}</option>
            ))}
          </select>
        ) : (
          <input
            aria-label={label}
            type={type}
            required
            autoFocus
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            className={cx(field, mono && 'font-mono text-small')}
          />
        )}
        <button type="submit" className={actionClass()}>
          Save
        </button>
        <TextAction onClick={() => setDraft(null)}>Cancel</TextAction>
      </form>
    </SettingRow>
  )
}

export function NotificationRow({
  title,
  checked,
  onChange,
}: {
  title: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <div className="flex min-h-[39px] items-center gap-4 border-b border-rule py-2">
      <p className="min-w-0 flex-1">{title}</p>
      <Switch checked={checked} onChange={onChange} label={title} />
    </div>
  )
}
