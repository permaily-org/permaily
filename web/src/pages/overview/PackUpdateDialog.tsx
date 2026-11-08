import { useState } from 'react'
import { Button, Dialog, Switch, TextLink, Tile } from '../../components/ui/index.ts'
import { connections } from '../../data/sample.ts'
import { packUpdate } from './data.ts'

export type PackDecision = { approvedFor: string[] }

/** Asks before a community pack update widens what connections can read. */
export function PackUpdateDialog({
  open,
  onClose,
  onDecide,
}: {
  open: boolean
  onClose: () => void
  onDecide: (decision: PackDecision) => void
}) {
  const affected = connections.filter((c) => packUpdate.connectionIds.includes(c.id))
  const [approved, setApproved] = useState<string[]>(() => affected.map((c) => c.id))
  const chosen = affected.filter((c) => approved.includes(c.id))

  const approveLabel =
    chosen.length === 0
      ? 'Approve'
      : chosen.length === affected.length
        ? affected.length === 2
          ? 'Approve for both'
          : 'Approve for all'
        : `Approve for ${chosen.map((c) => c.name).join(' and ')}`

  const toggle = (id: string, on: boolean) => setApproved((ids) => (on ? [...ids, id] : ids.filter((x) => x !== id)))
  const senderCount = packUpdate.senders.length

  return (
    <Dialog open={open} onClose={onClose} label={`${packUpdate.pack} update`}>
      <p className="font-mono text-mono text-ink-2">
        {packUpdate.pack}, version {packUpdate.from} to version {packUpdate.to}
      </p>
      <h2 className="mt-2 text-heading font-medium">
        A community pack wants to add {senderCount} {senderCount === 1 ? 'sender' : 'senders'}.
      </h2>
      <p className="mt-2 text-body text-ink-2">Packs can't widen your access on their own. Nothing changes until you approve it.</p>

      <ul className="mt-[18px] border-t border-rule-strong">
        {packUpdate.senders.map((sender) => (
          <li key={sender.domain} className="flex items-center justify-between gap-4 border-b border-rule py-2.5">
            <span className="bg-highlight px-1.5 py-0.5 font-mono text-mono text-on-highlight">
              <span aria-hidden>+ </span>
              <span className="sr-only">New sender </span>
              {sender.domain}
            </span>
            <span className="font-mono text-mono text-ink-2">
              {sender.matches} {sender.matches === 1 ? 'email' : 'emails'} would match
            </span>
          </li>
        ))}
      </ul>

      <ul className="mt-6 flex flex-col gap-4">
        {affected.map((c) => (
          <li key={c.id} className="flex items-center gap-3">
            <Tile initials={c.initials} />
            <span className="flex-1 text-body">{c.name}</span>
            <Switch
              checked={approved.includes(c.id)}
              onChange={(on) => toggle(c.id, on)}
              label={`Approve version ${packUpdate.to} for ${c.name}`}
            />
          </li>
        ))}
      </ul>

      <div className="mt-6 flex items-center gap-4">
        <span className="font-mono text-mono text-ink-2">Reviewed by {packUpdate.maintainers} maintainers</span>
        <TextLink to="/rules/packs">See the change</TextLink>
      </div>

      <div className="mt-5 flex flex-wrap gap-2.5">
        <Button disabled={chosen.length === 0} onClick={() => onDecide({ approvedFor: chosen.map((c) => c.name) })}>
          {approveLabel}
        </Button>
        <Button variant="secondary" onClick={() => onDecide({ approvedFor: [] })}>
          Keep version {packUpdate.from}
        </Button>
      </div>
    </Dialog>
  )
}
