import { useId, useState } from 'react'
import { Checkbox, InfoTip } from '../../components/ui/index.ts'
import { attachmentSuggestions, dataSuggestions, type Part, parts, type Removal } from './data.ts'
import { ValueList } from './Values.tsx'

/**
 * What a partial rule takes out: whole parts of the email, and for attachments and data, what to look for. Data is
 * described in words, such as "Home address", and attachments with none described means all of them.
 */
export function RemovalParts({
  removal,
  onChange,
}: {
  removal: Removal
  onChange: (removal: Removal) => void
}) {
  const titleId = useId()
  // Data needs something described, so switching it on opens its field.
  const [opened, setOpened] = useState<Part | null>(null)

  function toggle(part: Part, on: boolean) {
    if (on) setOpened(part)
    const next = on
      ? parts.map((p) => p.part).filter((p) => p === part || removal.parts.includes(p))
      : removal.parts.filter((p) => p !== part)
    onChange({ ...removal, parts: next })
  }

  return (
    <div role="group" aria-labelledby={titleId} className="w-full">
      <p id={titleId} className="text-small text-ink-2">
        Takes out
      </p>
      <ul className="mt-1.5 border-t border-rule">
        {parts.map(({ part, label, tip }) => {
          const on = removal.parts.includes(part)
          return (
            <li
              key={part}
              className="flex flex-col gap-2 border-b border-rule py-2 last:border-b-0 last:pb-0"
            >
              <span className="flex items-center gap-1.5">
                <Checkbox
                  label={label}
                  checked={on}
                  onChange={(event) => toggle(part, event.target.checked)}
                  className="text-small"
                />
                {tip && (
                  <InfoTip label={`About ${label.toLowerCase()}`} align="start">
                    {tip}
                  </InfoTip>
                )}
              </span>
              {/* What to look for sits under its part, lined up with the label. */}
              {on && part === 'data' && (
                <div className="pl-7">
                  <ValueList
                    label="Data"
                    values={removal.data}
                    onChange={(data) => onChange({ ...removal, data })}
                    placeholder="something, in your words"
                    suggestions={dataSuggestions}
                    autoOpen={opened === 'data' && removal.data.length === 0}
                  />
                </div>
              )}
              {on && part === 'attachments' && (
                <div className="pl-7">
                  <ValueList
                    label="Attachments"
                    values={removal.attachments}
                    onChange={(attachments) => onChange({ ...removal, attachments })}
                    placeholder="which ones, in your words"
                    suggestions={attachmentSuggestions}
                    empty="All"
                  />
                </div>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
