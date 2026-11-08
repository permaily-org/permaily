import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { Count, Headline, TermRow, TextAction } from '../../components/ui/index.ts'
import { formatCount, inbox, mailboxes, type Mailbox } from '../../data/sample.ts'
import { ChoiceTerm } from './ChoiceDialog.tsx'
import { ConsentLayout, Decision, Requester, Terms } from './ConsentLayout.tsx'
import { appPartsChoices, endChoices, endOptions, gmailShortQuery, mailSince, requesters, sharedLens, tripTimeline, viewRows, type QueryId } from './data.ts'
import { ForwardingPreview } from './ForwardingPreview.tsx'
import { InboxPreview } from './InboxPreview.tsx'
import { MailboxPicker } from './MailboxPicker.tsx'
import { ErrorPreview, LoadingPreview } from './PreviewStates.tsx'
import { usePreviewState } from './previewState.ts'

const app = requesters.tripApp
const asked: QueryId[] = ['flights-and-hotels']

export function ConsentAppPage() {
  const navigate = useNavigate()
  const { state, retry } = usePreviewState()
  const [mailboxId, setMailboxId] = useState<Mailbox['id']>('work')
  const [parts, setParts] = useState(appPartsChoices[0].value)
  const [end, setEnd] = useState<string>(endOptions[0])
  const [excluded, setExcluded] = useState<string[]>([])
  const focusAfter = useRef<string | null>(null)

  useEffect(() => {
    if (!focusAfter.current) return
    document.querySelector<HTMLElement>(focusAfter.current)?.focus()
    focusAfter.current = null
  }, [excluded])

  const exclude = (sender: string) => {
    focusAfter.current = '#undo-exclusion'
    setExcluded((list) => [...list, sender])
  }

  const undo = () => {
    focusAfter.current = '[aria-label="Change parts"]'
    setExcluded([])
  }

  const forwarding = mailboxes.find((m) => m.id === mailboxId)?.method === 'forwarding'
  const pending = !forwarding && state !== 'ready'
  const rows = useMemo(() => viewRows(tripTimeline, sharedLens(new Set(asked), new Set(excluded))), [excluded])
  const shared = rows.filter((row) => row.kind === 'shared').length

  const note = forwarding
    ? `Next you add ${app.name}'s senders to your Gmail filter, which takes about a minute. You can revoke this at any time.`
    : pending
      ? "You decide after you've seen the preview."
      : `You can revoke this at any time. Mail ${app.name} has already read can't be recalled.`

  return (
    <ConsentLayout
      mailbox={<MailboxPicker value={mailboxId} onChange={setMailboxId} />}
      preview={
        forwarding ? (
          <ForwardingPreview name={app.name} queries={asked} />
        ) : state === 'loading' ? (
          <LoadingPreview />
        ) : state === 'error' ? (
          <ErrorPreview onRetry={retry} />
        ) : (
          <InboxPreview
            eyebrow="Live preview, run on your own mail"
            headline={
              <>
                <Count>{formatCount(shared)}</Count>
                <span>of {formatCount(inbox.total)} emails</span>
              </>
            }
            caption={`Your inbox as ${app.name} would see it. ${formatCount(shared)} today, plus new matching mail until ${end}.`}
            rows={rows}
            focus="shared"
            switchLabel={`Only the ${formatCount(shared)} shared`}
            rowAction={(row) => (
              <TextAction aria-label={`Exclude sender ${row.sender}`} onClick={() => exclude(row.sender)}>
                Exclude sender
              </TextAction>
            )}
          />
        )
      }
    >
      <Requester initials={app.initials} line={`${app.domain} is asking for access`} facts={app.facts} />
      <Headline size="display-m" className="mt-6">Trip App wants your flights and hotels. Nothing else.</Headline>

      <Terms>
        <TermRow label="Asks for">Flight, hotel, rail and car bookings</TermRow>
        <TermRow label="Query">
          <code className="min-w-0 truncate font-mono text-mono text-ink-2">{gmailShortQuery(asked)}</code>
        </TermRow>
        {excluded.length > 0 && (
          <TermRow
            label="Except"
            action={
              <TextAction id="undo-exclusion" aria-label={`Undo excluding ${excluded.join(', ')}`} onClick={undo}>
                Undo
              </TextAction>
            }
          >
            {excluded.join(', ')}
          </TermRow>
        )}
        <ChoiceTerm label="Parts" title={`Parts ${app.name} can read`} choices={appPartsChoices} value={parts} onSave={setParts} />
        <TermRow label="Actions">Read only</TermRow>
        {forwarding ? (
          <TermRow label="Time">New mail only, until {end}</TermRow>
        ) : (
          <ChoiceTerm
            label="Time"
            title={`How long ${app.name} can read`}
            choices={endChoices(`What ${app.name} asked for`)}
            value={end}
            onSave={setEnd}
            display={`${mailSince}, ends ${end}`}
          />
        )}
      </Terms>

      <Decision
        allowLabel={`Allow ${app.name}`}
        onAllow={() => navigate('/connections/trip-app')}
        allowDisabled={pending}
        declineLabel="Not now"
        onDecline={() => navigate('/')}
        note={note}
      />
    </ConsentLayout>
  )
}
