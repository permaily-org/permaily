import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { Button, ButtonLink, Headline, InfoTip, Notice, TextAction, TextLink, Tile } from '../../components/ui/index.ts'
import { mailboxes as allMailboxes, type Mailbox } from '../../data/sample.ts'
import { cx, pagePadding } from '../../lib/styles.ts'
import { listNames, vendorsUsing } from './data.ts'
import { DisconnectDialog } from './DisconnectDialog.tsx'
import { attentionButton } from './styles.ts'

type PageState = 'filter-behind' | 'mailbox-error' | null

type Status = { label: string; detail: string; tone: 'ok' | 'attention' | 'danger' }

function statusOf(mailbox: Mailbox, state: PageState): Status {
  if (state === 'filter-behind' && mailbox.method === 'forwarding')
    return { label: 'Filter out of date', detail: '1 approved query not covered', tone: 'attention' }
  if (state === 'mailbox-error' && mailbox.method === 'google')
    return { label: 'Access ended', detail: 'Reconnect to resume', tone: 'danger' }
  return { label: mailbox.status, detail: mailbox.statusDetail, tone: 'ok' }
}

function headline(count: number): string {
  if (count === 0) return 'No mailboxes connected.'
  return count === 1 ? '1 mailbox connected.' : `${count} mailboxes connected.`
}

export function AccountsPage() {
  const [params] = useSearchParams()
  const raw = params.get('state')
  const state: PageState = raw === 'filter-behind' || raw === 'mailbox-error' ? raw : null
  const [mailboxes, setMailboxes] = useState(allMailboxes)
  const [disconnecting, setDisconnecting] = useState<Mailbox | null>(null)
  const errored = state === 'mailbox-error' ? mailboxes.find((m) => m.method === 'google') : undefined

  return (
    <div className={pagePadding}>
      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-4">
        <Headline>{headline(mailboxes.length)}</Headline>
        <ButtonLink to="/accounts/new">Connect a mailbox</ButtonLink>
      </div>

      {state === 'filter-behind' && mailboxes.some((m) => m.method === 'forwarding') && (
        <div className="mt-8 mb-2">
          <Notice
            title="Your Gmail filter is behind your connections."
            action={
              <Link to="/accounts/new/forwarding" className={attentionButton}>
                Update filter
              </Link>
            }
          >
            New mail from norwegian.com, approved for Trip App, isn't reaching Permaily.
          </Notice>
        </div>
      )}

      {errored && <MailboxError mailbox={errored} onRemove={() => setDisconnecting(errored)} />}

      <table role="table" className="mt-6 block w-full md:table border-collapse text-left">
        <thead role="rowgroup" className="max-md:sr-only">
          <tr role="row" className="border-b border-rule-strong text-small text-ink-2">
            <th scope="col" role="columnheader" className="w-[216px] pb-2.5 font-normal">
              Mailbox
            </th>
            <th scope="col" role="columnheader" className="hidden w-[260px] pb-2.5 font-normal xl:table-cell">
              History
            </th>
            <th scope="col" role="columnheader" className="hidden w-[136px] pb-2.5 font-normal xl:table-cell">
              Actions
            </th>
            <th scope="col" role="columnheader" className="w-[136px] pb-2.5 font-normal">
              Used by
            </th>
            <th scope="col" role="columnheader" className="pb-2.5 font-normal">
              Status
            </th>
            <th scope="col" role="columnheader" className="pb-2.5">
              <span className="sr-only">Disconnect</span>
            </th>
          </tr>
        </thead>
        <tbody role="rowgroup" className="max-md:block">
          {mailboxes.map((mailbox) => (
            <MailboxRow key={mailbox.id} mailbox={mailbox} status={statusOf(mailbox, state)} onDisconnect={() => setDisconnecting(mailbox)} />
          ))}
        </tbody>
      </table>

      <DisconnectDialog
        mailbox={disconnecting}
        onCancel={() => setDisconnecting(null)}
        onConfirm={(mailbox) => {
          setMailboxes((list) => list.filter((m) => m.id !== mailbox.id))
          setDisconnecting(null)
        }}
      />
    </div>
  )
}

function MailboxRow({ mailbox, status, onDisconnect }: { mailbox: Mailbox; status: Status; onDisconnect: () => void }) {
  const users = vendorsUsing(mailbox)
  return (
    <tr role="row" className="border-b border-rule align-middle max-md:grid max-md:grid-cols-[1fr_auto] max-md:items-start max-md:gap-x-4 max-md:gap-y-2 max-md:py-3">
      <td role="cell" className="py-3 pr-4 max-md:col-start-1 max-md:row-start-1 max-md:min-w-0 max-md:p-0">
        <div className="font-mono text-small break-all">{mailbox.address}</div>
        <div className="mt-0.5 flex items-center gap-1.5 text-small text-ink-2">
          {mailbox.label}
          <InfoTip label={`What Permaily receives from ${mailbox.address}`} align="start">
            {mailbox.receives}
          </InfoTip>
        </div>
      </td>
      <td role="cell" className="hidden py-3 pr-4 xl:table-cell">
        <div className="flex items-center gap-2.5">
          {mailbox.history}
          {mailbox.method === 'forwarding' && (
            <TextLink to="/accounts/new" size="sm">
              Add older mail
            </TextLink>
          )}
        </div>
      </td>
      <td role="cell" className="hidden py-3 pr-4 whitespace-nowrap xl:table-cell">{mailbox.actions}</td>
      <td role="cell" className="py-3 pr-4 max-md:col-start-2 max-md:row-start-2 max-md:p-0">
        <span className="flex gap-1 max-md:justify-end">
          {users.map((c) => (
            <Tile key={c.id} initials={c.initials} size="sm" />
          ))}
        </span>
        <span className="sr-only">{users.length ? listNames(users.map((c) => c.name)) : 'No connections'}</span>
      </td>
      <td role="cell" className="py-3 pr-4 max-md:col-start-1 max-md:row-start-2 max-md:p-0">
        <div className={cx(status.tone === 'ok' && 'text-ok', status.tone === 'attention' && 'text-attention', status.tone === 'danger' && 'text-danger')}>
          {status.label}
        </div>
        <div className="mt-0.5 text-small text-ink-2">{status.detail}</div>
      </td>
      <td role="cell" className="py-3 text-right max-md:col-start-2 max-md:row-start-1 max-md:p-0">
        <TextAction tone="danger" onClick={onDisconnect} aria-label={`Disconnect ${mailbox.address}`}>
          Disconnect
        </TextAction>
      </td>
    </tr>
  )
}

function MailboxError({ mailbox, onRemove }: { mailbox: Mailbox; onRemove: () => void }) {
  const navigate = useNavigate()
  return (
    <section aria-labelledby="mailbox-error" className="mt-10 mb-4 max-w-[640px]">
      <h2 id="mailbox-error" className="text-heading font-medium">
        Google ended Permaily's access.
      </h2>
      <p className="mt-3 text-body text-ink-2">
        Connections can't get new mail from <span className="font-mono text-small text-ink">{mailbox.address}</span> until you reconnect.
      </p>
      <div className="mt-5 flex flex-wrap gap-3">
        <Button onClick={() => navigate('/accounts')}>Reconnect</Button>
        <Button variant="secondary" onClick={onRemove}>
          Remove this mailbox
        </Button>
      </div>
    </section>
  )
}
