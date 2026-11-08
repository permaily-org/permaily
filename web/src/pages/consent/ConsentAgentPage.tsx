import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { Checkbox, Count, Headline, TermRow, TextLink } from '../../components/ui/index.ts'
import { formatCount, inbox, mailboxes, type Mailbox } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'
import { ChoiceTerm } from './ChoiceDialog.tsx'
import { ConsentLayout, Decision, Requester, Terms } from './ConsentLayout.tsx'
import {
  agentActionChoices,
  agentPartsChoices,
  agentRuleIds,
  agentTimeline,
  endChoices,
  endOptions,
  mailSince,
  neverText,
  requesters,
  ruleCount,
  ruleName,
  sharedLens,
  viewRows,
  type RuleId,
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
  const [ticked, setTicked] = useState<RuleId[]>([])
  const [parts, setParts] = useState(agentPartsChoices[0].value)
  const [actions, setActions] = useState(agentActionChoices[0].value)
  const [end, setEnd] = useState<string>(endOptions[0])

  const chosen = agentRuleIds.filter((id) => ticked.includes(id))
  const total = chosen.reduce((sum, id) => sum + ruleCount(id), 0)
  const forwarding = mailboxes.find((m) => m.id === mailboxId)?.method === 'forwarding'
  const pending = !forwarding && state !== 'ready'
  const rows = useMemo(() => viewRows(agentTimeline, sharedLens(new Set(ticked))), [ticked])

  const toggle = (id: RuleId) => setTicked((list) => (list.includes(id) ? list.filter((r) => r !== id) : [...list, id]))

  const note =
    total === 0
      ? 'Choose at least one rule first. Connecting alone shares no mail.'
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
          <ForwardingPreview name={agent.name} rules={chosen} />
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
                : `Your inbox as ${agent.name} sees it now. Tick a rule and its mail appears here.`
            }
            rows={rows}
            focus="shared"
            switchLabel={`Only the ${formatCount(total)} shared`}
          />
        )
      }
    >
      <Requester initials={agent.initials} line={`${agent.domain} is asking to connect`} facts={agent.facts} />
      <Headline size="display-m" className="mt-6">
        <span className="w-full">Claude starts with nothing.</span>
        <span className="w-full">You choose what it sees.</span>
      </Headline>

      <div role="group" aria-label={`Rules ${agent.name} can read`} className="mt-5 border-t border-rule-strong">
        {agentRuleIds.map((id) => {
          const checked = ticked.includes(id)
          return (
            <div key={id} className="flex border-b border-rule py-2">
              <Checkbox
                className="w-full"
                checked={checked}
                onChange={() => toggle(id)}
                label={
                  <span className="flex items-center justify-between gap-4">
                    <span>{ruleName(id)}</span>
                    <span className={cx('font-mono text-mono', checked ? 'text-ink' : 'text-ink-2')}>
                      <span className="sr-only">, </span>
                      {formatCount(ruleCount(id))}
                      <span className="sr-only"> emails</span>
                    </span>
                  </span>
                }
              />
            </div>
          )
        })}
      </div>

      <TextLink to="/rules/new" className="mt-5">
        Write a new rule
      </TextLink>

      <Terms strong={false}>
        <ChoiceTerm label="Parts" title={`Parts ${agent.name} can read`} choices={agentPartsChoices} value={parts} onSave={setParts} />
        <ChoiceTerm label="Actions" title={`What ${agent.name} can do`} choices={agentActionChoices} value={actions} onSave={setActions} />
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
        <TermRow label="Never">{neverText}</TermRow>
      </Terms>

      <Decision
        allowLabel={total && !forwarding ? `Allow ${agent.name} to read ${formatCount(total)} emails` : `Allow ${agent.name}`}
        onAllow={() => navigate('/connections/claude')}
        allowDisabled={total === 0 || pending}
        declineLabel="Not now"
        onDecline={() => navigate('/')}
        note={note}
      />
    </ConsentLayout>
  )
}
