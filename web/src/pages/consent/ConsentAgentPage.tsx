import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { Checkbox, Count, Headline, TermRow } from '../../components/ui/index.ts'
import { formatCount, inbox, type Mailbox, mailboxes } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'
import { ChoiceTerm } from './ChoiceDialog.tsx'
import { ConsentLayout, Decision, Requester, Terms } from './ConsentLayout.tsx'
import {
  agentActionChoices,
  agentPartsChoices,
  agentQueryIds,
  agentTimeline,
  endChoices,
  endOptions,
  mailSince,
  type QueryId,
  queryCount,
  queryName,
  requesters,
  sharedLens,
  viewRows,
} from './data.ts'
import { ForwardingPreview } from './ForwardingPreview.tsx'
import { InboxPreview } from './InboxPreview.tsx'
import { MailboxPicker } from './MailboxPicker.tsx'
import { ErrorPreview, LoadingPreview } from './PreviewStates.tsx'
import { usePreviewState } from './previewState.ts'

const agent = requesters.claude

export function ConsentAgentPage() {
  const navigate = useNavigate()
  const { state, retry } = usePreviewState()
  const [mailboxId, setMailboxId] = useState<Mailbox['id']>('work')
  const [ticked, setTicked] = useState<QueryId[]>([])
  const [parts, setParts] = useState(agentPartsChoices[0].value)
  const [actions, setActions] = useState(agentActionChoices[0].value)
  const [end, setEnd] = useState<string>(endOptions[0])

  const chosen = agentQueryIds.filter((id) => ticked.includes(id))
  const total = chosen.reduce((sum, id) => sum + queryCount(id), 0)
  const forwarding = mailboxes.find((m) => m.id === mailboxId)?.method === 'forwarding'
  const pending = !forwarding && state !== 'ready'
  const rows = useMemo(() => viewRows(agentTimeline, sharedLens(new Set(ticked))), [ticked])

  const toggle = (id: QueryId) =>
    setTicked((list) => (list.includes(id) ? list.filter((r) => r !== id) : [...list, id]))

  const note =
    total === 0
      ? 'Choose at least one kind of mail first. Connecting alone shares no mail.'
      : forwarding
        ? `Next you add ${agent.name}'s senders to your Gmail filter, which takes about a minute. You can revoke this at any time.`
        : pending
          ? "You decide after you've seen the preview."
          : `You can change or revoke this at any time. Mail ${agent.name} has already read can't be recalled.`

  return (
    <ConsentLayout
      mailbox={<MailboxPicker value={mailboxId} onChange={setMailboxId} />}
      preview={
        forwarding ? (
          <ForwardingPreview name={agent.name} queries={chosen} />
        ) : state === 'loading' ? (
          <LoadingPreview />
        ) : state === 'error' ? (
          <ErrorPreview onRetry={retry} />
        ) : (
          <InboxPreview
            eyebrow="Live preview, run on your own mail"
            headline={
              <>
                <Count tone={total ? 'shared' : 'bar'}>{formatCount(total)}</Count>
                <span>of {formatCount(inbox.total)} emails</span>
              </>
            }
            caption={
              total
                ? `${formatCount(total)} today, plus new matching mail until ${end}.`
                : `Your inbox as ${agent.name} sees it now. Tick a kind of mail and it appears here.`
            }
            rows={rows}
            focus="shared"
            switchLabel={`Only the ${formatCount(total)} shared`}
          />
        )
      }
    >
      <Requester
        initials={agent.initials}
        line={`${agent.domain} is asking to connect`}
        facts={agent.facts}
      />
      <Headline size="display-m" className="mt-6">
        <span className="w-full">Claude starts with nothing.</span>
        <span className="w-full">You choose what it can search.</span>
      </Headline>

      <p className="mt-3 text-ink-2">
        {agent.name} writes its own searches. They only ever reach the kinds of mail you tick.
      </p>
      <div
        role="group"
        aria-label={`Mail ${agent.name} can search`}
        className="mt-4 border-t border-rule-strong"
      >
        {agentQueryIds.map((id) => {
          const checked = ticked.includes(id)
          return (
            <div key={id} className="flex border-b border-rule">
              {/* The padding sits on the label, so the whole row is the touch target. */}
              <Checkbox
                className="w-full py-2"
                checked={checked}
                onChange={() => toggle(id)}
                label={
                  <span className="flex items-center justify-between gap-4">
                    <span>{queryName(id)}</span>
                    <span
                      className={cx('font-mono text-mono', checked ? 'text-ink' : 'text-ink-2')}
                    >
                      <span className="sr-only">, </span>
                      {formatCount(queryCount(id))}
                      <span className="sr-only"> emails</span>
                    </span>
                  </span>
                }
              />
            </div>
          )
        })}
      </div>

      <Terms strong={false}>
        <ChoiceTerm
          label="Parts"
          title={`Parts ${agent.name} can read`}
          choices={agentPartsChoices}
          value={parts}
          onSave={setParts}
        />
        <ChoiceTerm
          label="Actions"
          title={`What ${agent.name} can do`}
          choices={agentActionChoices}
          value={actions}
          onSave={setActions}
        />
        {forwarding ? (
          <TermRow label="Time">New mail only, until {end}</TermRow>
        ) : (
          <ChoiceTerm
            label="Time"
            title={`How long ${agent.name} can read`}
            choices={endChoices('The usual length for a new connection')}
            value={end}
            onSave={setEnd}
            display={`${mailSince}, ends ${end}`}
          />
        )}
      </Terms>

      <Decision
        allowLabel={
          total && !forwarding
            ? `Allow ${agent.name} to read ${formatCount(total)} emails`
            : `Allow ${agent.name}`
        }
        onAllow={() => navigate('/connections/claude/chat')}
        allowDisabled={total === 0 || pending}
        declineLabel="Not now"
        onDecline={() => navigate('/')}
        note={note}
      />
    </ConsentLayout>
  )
}
