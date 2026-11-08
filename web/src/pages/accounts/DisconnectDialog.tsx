import { Button, Dialog, Tile } from '../../components/ui/index.ts'
import { vendorOf, type Mailbox } from '../../data/sample.ts'
import { connectionsUsing } from './data.ts'
import { solidDanger } from './styles.ts'

export function DisconnectDialog({
  mailbox,
  onCancel,
  onConfirm,
}: {
  mailbox: Mailbox | null
  onCancel: () => void
  onConfirm: (mailbox: Mailbox) => void
}) {
  const losing = mailbox ? connectionsUsing(mailbox) : []
  const kind = mailbox?.id === 'work' ? 'work' : 'personal'

  return (
    <Dialog open={mailbox !== null} onClose={onCancel} label="Disconnect mailbox">
      {mailbox && (
        <>
          <h2 className="text-heading font-medium">Disconnect your {kind} mailbox?</h2>
          <p className="mt-2 font-mono text-small">{mailbox.address}</p>

          <p className="mt-6 text-body">
            {losing.length === 0
              ? 'No connections use this mailbox.'
              : `${losing.length === 1 ? '1 connection loses' : `${losing.length} connections lose`} access to it now:`}
          </p>
          {losing.length > 0 && (
            <ul className="mt-2 border-t border-rule">
              {losing.map((connection) => (
                <li key={connection.id} className="flex items-center gap-3 border-b border-rule py-2">
                  <Tile initials={vendorOf(connection).initials} size="sm" />
                  <span className="font-medium">{connection.name}</span>
                  <span className="text-small text-ink-2">{vendorOf(connection).name}</span>
                </li>
              ))}
            </ul>
          )}
          <p className="mt-4 text-small text-ink-2">
            Stored tokens and queued mail are deleted. Your receipts stay until you remove them.
          </p>

          <div className="mt-6 flex flex-wrap gap-3">
            <button type="button" className={solidDanger} onClick={() => onConfirm(mailbox)}>
              Disconnect mailbox
            </button>
            <Button variant="secondary" onClick={onCancel}>
              Keep it connected
            </Button>
          </div>
        </>
      )}
    </Dialog>
  )
}
