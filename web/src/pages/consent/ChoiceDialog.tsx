import { useId, useState } from 'react'
import { Button, Dialog, TermRow, TextAction } from '../../components/ui/index.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import type { Choice } from './data.ts'

type ChoiceProps = {
  title: string
  choices: Choice[]
  value: string
  onSave: (value: string) => void
}

function ChoiceDialog({
  open,
  onClose,
  title,
  choices,
  value,
  onSave,
}: ChoiceProps & { open: boolean; onClose: () => void }) {
  const [draft, setDraft] = useState(value)
  const name = useId()
  return (
    <Dialog open={open} onClose={onClose} label={title}>
      <form
        onSubmit={(event) => {
          event.preventDefault()
          onSave(draft)
        }}
      >
        <fieldset>
          <legend className="text-title font-medium">{title}</legend>
          <div className="mt-4 border-t border-rule-strong">
            {choices.map((choice) => (
              <label
                key={choice.value}
                className="flex cursor-pointer items-start gap-3 border-b border-rule py-3"
              >
                <input
                  type="radio"
                  name={name}
                  value={choice.value}
                  checked={draft === choice.value}
                  onChange={() => setDraft(choice.value)}
                  className={cx('mt-0.5 size-4 shrink-0 cursor-pointer accent-ink', focusRing)}
                />
                <span className="flex flex-col gap-0.5">
                  <span>{choice.label}</span>
                  {choice.note && <span className="text-small text-ink-2">{choice.note}</span>}
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <div className="mt-6 flex gap-2.5">
          <Button type="submit">Save</Button>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

/** A term with a Change action that opens a short list of choices. */
export function ChoiceTerm({
  label,
  display,
  ...props
}: ChoiceProps & { label: string; display?: string }) {
  const [open, setOpen] = useState(false)
  const [session, setSession] = useState(0)
  return (
    <>
      <TermRow
        label={label}
        action={
          <TextAction
            aria-label={`Change ${label.toLowerCase()}`}
            onClick={() => {
              setSession((s) => s + 1)
              setOpen(true)
            }}
          >
            Change
          </TextAction>
        }
      >
        {display ?? props.value}
      </TermRow>
      <ChoiceDialog
        key={session}
        {...props}
        open={open}
        onClose={() => setOpen(false)}
        onSave={(value) => {
          props.onSave(value)
          setOpen(false)
        }}
      />
    </>
  )
}
