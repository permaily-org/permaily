import { Check } from 'lucide-react'
import { useState } from 'react'
import { Button, Notice } from '../../components/ui/index.ts'
import type { Connection, Receipt, Vendor } from '../../data/sample.ts'
import { deletionRequest, revokedAt } from './DetailData.ts'

type CopyState = 'idle' | 'copied' | 'failed'

/** Says what revoking didn't do, and writes the deletion request for the person. */
export function DetailRevokedNotice({
  vendor,
  connection,
  items,
  headersOnly,
}: {
  vendor: Vendor
  connection: Connection
  items: Receipt[]
  headersOnly: boolean
}) {
  const [copy, setCopy] = useState<CopyState>('idle')
  const held = connection.hasRead > 0
  const request = deletionRequest(vendor, connection, items, headersOnly)

  async function copyRequest() {
    try {
      await navigator.clipboard.writeText(request)
      setCopy('copied')
    } catch {
      setCopy('failed')
    }
  }

  const action = held && (
    <Button variant="secondary" size="sm" onClick={copyRequest}>
      {copy === 'copied' && <Check aria-hidden size={14} />}
      {copy === 'copied' ? 'Copied' : 'Copy a deletion request'}
    </Button>
  )

  return (
    <div className="mt-4">
      <Notice tone="neutral" title={`Access ended at ${revokedAt}.`} action={action}>
        {held
          ? `Permaily can't recall what it already read. ${vendor.name} can delete it.`
          : undefined}
      </Notice>
      {copy === 'failed' && (
        <label className="mt-3 block">
          <span className="text-small text-ink-2">
            Couldn't copy. Select the request and copy it yourself.
          </span>
          <textarea
            readOnly
            value={request}
            rows={8}
            onFocus={(event) => event.currentTarget.select()}
            className="mt-2 block w-full resize-y rounded-sm border border-rule bg-raised p-3 text-small text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink"
          />
        </label>
      )}
    </div>
  )
}
