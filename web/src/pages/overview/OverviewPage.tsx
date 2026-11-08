import { useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ButtonLink, Count, FilterButton, Headline } from '../../components/ui/index.ts'
import { attention, connections, formatCount, inbox } from '../../data/sample.ts'
import { SuggestedRule } from '../rules/SuggestedRule.tsx'
import { AccessGrid } from './AccessGrid.tsx'
import { AttentionList } from './AttentionList.tsx'
import { packUpdate } from './data.ts'
import { PackUpdateDialog, type PackDecision } from './PackUpdateDialog.tsx'

export function OverviewPage() {
  const [params] = useSearchParams()
  return params.get('state') === 'first-use' ? <FirstUse /> : <Overview />
}

function Overview() {
  const [packOpen, setPackOpen] = useState(false)
  const [settled, setSettled] = useState<string[]>([])
  const [announcement, setAnnouncement] = useState('')
  const attentionRef = useRef<HTMLElement>(null)
  const items = attention.filter((item) => !settled.includes(item.id))

  const headerReaders = connections.filter((c) => c.cells.includes('headers')).map((c) => c.name)

  function decide({ approvedFor }: PackDecision) {
    setSettled((ids) => [...ids, packUpdate.attentionId])
    setPackOpen(false)
    setAnnouncement(
      approvedFor.length > 0
        ? `${packUpdate.pack} version ${packUpdate.to} approved for ${approvedFor.join(' and ')}.`
        : `Kept ${packUpdate.pack} version ${packUpdate.from}.`,
    )
    requestAnimationFrame(() => attentionRef.current?.focus())
  }

  return (
    <div className="px-14 pt-9 pb-14">
      <Headline>
        {connections.length} connections can read <Count>{formatCount(inbox.readable)}</Count> of your {formatCount(inbox.total)}{' '}
        emails.
      </Headline>
      <div className="mt-2 flex items-center justify-between gap-6">
        <p className="text-lead text-ink-2">
          {headerReaders.length > 0 &&
            `${headerReaders.join(' and ')} also ${headerReaders.length === 1 ? 'sees' : 'see'} the headers of ${formatCount(inbox.headersOnly)} emails. `}
          Your rules withhold {formatCount(inbox.withheld)} from every connection.
        </p>
        <FilterButton>All mailboxes</FilterButton>
      </div>

      <div className="mt-6">
        <AccessGrid />
      </div>

      <div className="mt-[26px]">
        <AttentionList ref={attentionRef} items={items} onOpenPackUpdate={() => setPackOpen(true)} />
      </div>
      <p role="status" className="sr-only">
        {announcement}
      </p>

      <PackUpdateDialog open={packOpen} onClose={() => setPackOpen(false)} onDecide={decide} />
    </div>
  )
}

function FirstUse() {
  const [suggest, setSuggest] = useState(true)
  return (
    <div className="px-14 pt-9 pb-14">
      <Headline>Nothing can read your mail.</Headline>
      <p className="mt-3 max-w-[640px] text-lead text-ink-2">
        Your mailbox is connected. Every connection starts with nothing, until you say otherwise.
      </p>
      <div role="img" aria-label="Every kind of mail is withheld from every connection" className="mt-6 grid max-w-[560px] grid-cols-5 gap-1">
        {Array.from({ length: 15 }, (_, i) => (
          <span key={i} className="h-7 bg-bar" />
        ))}
      </div>
      {suggest && (
        <div className="mt-8 max-w-[560px]">
          <SuggestedRule onDismiss={() => setSuggest(false)} />
        </div>
      )}
      <div className="mt-6">
        <ButtonLink to="/connections/new">Connect an agent</ButtonLink>
      </div>
    </div>
  )
}
