import { Clock, Webhook } from 'lucide-react'
import { type Trigger, triggerShort, triggerText } from '../../data/sample.ts'

/**
 * When a connection's query runs. A webhook icon marks the ones that run on new mail. In a list the
 * icon keeps its slot so labels line up. On its own, a label without an icon starts flush.
 */
export function Runs({ trigger, long = false }: { trigger: Trigger; long?: boolean }) {
  const Icon =
    trigger.kind === 'new-mail' ? Webhook : trigger.kind === 'schedule' ? Clock : undefined
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      {(Icon || !long) && (
        <span className="inline-flex w-3.5 shrink-0 text-ink-2">
          {Icon && <Icon aria-hidden size={14} strokeWidth={1.75} />}
        </span>
      )}
      <span className="truncate">{long ? triggerText(trigger) : triggerShort(trigger)}</span>
    </span>
  )
}
