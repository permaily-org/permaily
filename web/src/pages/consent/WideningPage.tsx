import { useState } from 'react'
import { useNavigate } from 'react-router'
import { Count, Headline, Tag, TermRow, TextAction } from '../../components/ui/index.ts'
import { connectionById, formatCount } from '../../data/sample.ts'
import { ConsentLayout, Decision, Requester, Terms } from './ConsentLayout.tsx'
import { ledgerlyTimeline, neverText, requesters, rulePack, viewRows, wideningLens, type RuleId } from './data.ts'
import { InboxPreview } from './InboxPreview.tsx'
import { MailboxLabel } from './MailboxPicker.tsx'
import { ErrorPreview, LoadingPreview } from './PreviewStates.tsx'
import { usePreviewState } from './previewState.ts'

const app = requesters.ledgerly
const connection = connectionById('ledgerly')
const existing = new Set<RuleId>(['invoices', 'receipts'])
const added = new Set<RuleId>(['bank-statements'])
const rows = viewRows(ledgerlyTimeline, wideningLens(existing, added))
const newCount = rows.filter((row) => row.kind === 'new').length
const currentCount = rows.filter((row) => row.kind === 'soft').length

export function WideningPage() {
  const navigate = useNavigate()
  const { state, retry } = usePreviewState()
  const [showRest, setShowRest] = useState(false)
  const pending = state !== 'ready'

  return (
    <ConsentLayout
      mailbox={<MailboxLabel id="work" />}
      preview={
        state === 'loading' ? (
          <LoadingPreview />
        ) : state === 'error' ? (
          <ErrorPreview onRetry={retry} />
        ) : (
          <InboxPreview
            eyebrow="Live preview, run on your own mail"
            headline={
              <>
                <Count>+{formatCount(newCount)}</Count>
                <span>on top of {formatCount(currentCount)}</span>
              </>
            }
            caption={`Tagged rows are what's new. Pale rows are what ${app.name} can already read.`}
            rows={rows}
            focus="new"
            switchLabel={`Only the ${formatCount(newCount)} new`}
          />
        )
      }
    >
      <Requester initials={app.initials} line={`${app.domain} has access and is asking for more`} facts={app.facts} />
      <Headline size="display-m" className="mt-6">Ledgerly wants to add your bank statements.</Headline>

      <Terms>
        <TermRow label="Adds" action={<Tag>{rulePack('bank-statements')}</Tag>}>
          Bank and card statements
        </TermRow>
        <TermRow
          label="The rest"
          action={
            <TextAction aria-expanded={showRest} aria-controls="unchanged-terms" onClick={() => setShowRest((v) => !v)}>
              {showRest ? 'Hide' : 'Show'}
            </TextAction>
          }
        >
          Parts, actions, time and what's withheld stay the same.
        </TermRow>
        <div id="unchanged-terms" hidden={!showRest} className="text-ink-2">
          <TermRow label="Emails">{connection?.ruleNames}</TermRow>
          <TermRow label="Parts">{connection?.parts}</TermRow>
          <TermRow label="Actions">{connection?.actions}</TermRow>
          <TermRow label="Time">{connection?.time}</TermRow>
          <TermRow label="Never">{neverText}</TermRow>
        </div>
      </Terms>

      <Decision
        allowLabel="Allow the addition"
        onAllow={() => navigate('/connections/ledgerly')}
        allowDisabled={pending}
        declineLabel="Keep it as it is"
        onDecline={() => navigate('/')}
        note={pending ? "You decide after you've seen the preview." : `Saying no changes nothing. ${app.name} keeps the access it already has.`}
      />
    </ConsentLayout>
  )
}
