import { useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { Button, ButtonLink, Headline, Notice, TextAction, TextLink, Tile } from '../../components/ui/index.ts'
import { mailboxes as allMailboxes, type Mailbox } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'
import { connectionsUsing, listNames } from './data.ts'
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
    <div className="px-14 pt-9 pb-16">
      <div className="flex items-center justify-between gap-6">
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
            You approved norwegian.com for Trip App, so new mail from it isn't reaching Permaily. Nothing extra is being shared.
          </Notice>
        </div>
      )}

      {errored && <MailboxError mailbox={errored} onRemove={() => setDisconnecting(errored)} />}

      <table className="mt-6 w-full border-collapse text-left">
        <thead>
          <tr className="border-b border-rule-strong text-small text-ink-2">
            <th scope="col" className="w-[216px] pb-2.5 font-normal">
              Mailbox
            </th>
            <th scope="col" className="w-[338px] pb-2.5 font-normal">
              What Permaily receives
            </th>
            <th scope="col" className="w-[136px] pb-2.5 font-normal">
              Actions
            </th>
            <th scope="col" className="w-[136px] pb-2.5 font-normal">
              Used by
            </th>
            <th scope="col" className="pb-2.5 font-normal">
              Status
            </th>
            <th scope="col" className="pb-2.5">
              <span className="sr-only">Disconnect</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {mailboxes.map((mailbox) => (
            <MailboxRow key={mailbox.id} mailbox={mailbox} status={statusOf(mailbox, state)} onDisconnect={() => setDisconnecting(mailbox)} />
          ))}
        </tbody>
      </table>

      <div className="mt-8">
        <p className="font-medium">Disconnecting a mailbox stops everything it feeds.</p>
        <p className="mt-1 text-small text-ink-2">
          Connections lose access at once, stored tokens and queued mail are deleted, and your receipts stay until you remove them.
        </p>
      </div>

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
  const users = connectionsUsing(mailbox)
  return (
    <tr className="border-b border-rule align-middle">
      <td className="py-3 pr-4">
        <div className="font-mono text-small">{mailbox.address}</div>
        <div className="mt-0.5 text-small text-ink-2">{mailbox.label}</div>
      </td>
      <td className="py-3 pr-4">
        <div>{mailbox.receives}</div>
        <div className="mt-0.5 flex items-center gap-2.5 text-small text-ink-2">
          {mailbox.history}
          {mailbox.method === 'forwarding' && (
            <TextLink to="/accounts/new" size="sm">
              Add older mail
            </TextLink>
          )}
        </div>
      </td>
      <td className="py-3 pr-4 whitespace-nowrap">{mailbox.actions}</td>
      <td className="py-3 pr-4">
        <span className="flex gap-1">
          {users.map((c) => (
            <Tile key={c.id} initials={c.initials} size="sm" />
          ))}
        </span>
        <span className="sr-only">{users.length ? listNames(users.map((c) => c.name)) : 'No connections'}</span>
      </td>
      <td className="py-3 pr-4">
        <div className={cx(status.tone === 'ok' && 'text-ok', status.tone === 'attention' && 'text-attention', status.tone === 'danger' && 'text-danger')}>
          {status.label}
        </div>
        <div className="mt-0.5 text-small text-ink-2">{status.detail}</div>
      </td>
      <td className="py-3 text-right">
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
        Connections can't get new mail from <span className="font-mono text-small text-ink">{mailbox.address}</span> until you reconnect. This often
        follows a password change.
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
