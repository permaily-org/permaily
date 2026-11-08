import { useId, useState, type FormEvent } from 'react'
import { Button, Dialog } from '../../components/ui/index.ts'
import type { Pack } from '../../data/sample.ts'
import { focusRing } from '../../lib/styles.ts'
import type { PackSource } from './data.ts'

function sourceText(pack: Pack, source: PackSource): string {
  const lines = [`id: ${pack.id}`, `version: ${source.version}`, `description: ${pack.description}`, 'match:', '  any:']
  if (source.data) lines.push(`    - data: [${source.data.machine.join(', ')}]`)
  const all: string[] = []
  if (source.senders) all.push(`from: [${source.senders.join(', ')}]`)
  if (source.subject) all.push(`subject_has: [${source.subject.join(', ')}]`)
  if (source.header) all.push(`header: [${source.header.join(', ')}]`)
  if (all.length === 1) lines.push(`    - ${all[0]}`)
  if (all.length > 1) lines.push('    - all:', ...all.map((line) => `        - ${line}`))
  lines.push('checks:', '  - no_free_mail_domains', '  - no_wildcard_senders')
  return lines.join('\n')
}

export function SourceDialog({ pack, source, onClose }: { pack: Pack; source: PackSource; onClose: () => void }) {
  return (
    <Dialog open onClose={onClose} label={`Source of ${pack.id}`}>
      <p className="font-mono text-mono text-ink-2">
        {pack.id}.yaml, version {source.version}
      </p>
      <h2 className="mt-1 text-title font-medium">Pack source</h2>
      <pre className="mt-4 max-h-[60vh] overflow-auto border-y border-rule py-4 font-mono text-mono break-words whitespace-pre-wrap">
        {sourceText(pack, source)}
      </pre>
      <p className="mt-3 text-small text-ink-2">
        Anyone can read this file and propose a change. {source.reviewedBy} maintainers review each change before a new version ships.
      </p>
      <div className="mt-6 flex justify-end">
        <Button variant="secondary" onClick={onClose}>
          Close
        </Button>
      </div>
    </Dialog>
  )
}

export function ProposeDialog({
  title,
  fieldLabel,
  placeholder,
  initial = '',
  sentText,
  onClose,
}: {
  title: string
  fieldLabel: string
  placeholder: string
  initial?: string
  sentText: (value: string) => string
  onClose: () => void
}) {
  const [value, setValue] = useState(initial)
  const [sent, setSent] = useState<string | null>(null)
  const fieldId = useId()

  function submit(event: FormEvent) {
    event.preventDefault()
    const trimmed = value.trim()
    if (trimmed) setSent(trimmed)
  }

  return (
    <Dialog open onClose={onClose} label={title}>
      <h2 className="text-title font-medium">{title}</h2>
      {sent ? (
        <>
          <p role="status" className="mt-3 text-body">
            {sentText(sent)}
          </p>
          <div className="mt-6 flex justify-end">
            <Button variant="secondary" onClick={onClose}>
              Close
            </Button>
          </div>
        </>
      ) : (
        <form onSubmit={submit}>
          <label htmlFor={fieldId} className="mt-4 block text-small text-ink-2">
            {fieldLabel}
          </label>
          <input
            id={fieldId}
            value={value}
            onChange={(event) => setValue(event.target.value)}
            placeholder={placeholder}
            autoFocus
            className={`mt-1.5 w-full rounded-sm border border-ink bg-transparent px-3 py-2 text-body placeholder:text-ink-3 ${focusRing}`}
          />
          <p className="mt-2 text-small text-ink-2">Maintainers check every proposal before it reaches anyone, and nothing changes for you until you approve it.</p>
          <div className="mt-6 flex justify-end gap-3">
            <Button variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!value.trim()}>
              Send proposal
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}
