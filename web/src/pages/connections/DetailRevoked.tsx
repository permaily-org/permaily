import { Check } from 'lucide-react'
import { useState } from 'react'
import { Button, Notice } from '../../components/ui/index.ts'
import type { Connection, Receipt } from '../../data/sample.ts'
import { deletionRequest, revokedAt } from './DetailData.ts'

type CopyState = 'idle' | 'copied' | 'failed'

/** Says what revoking did and didn't do, and writes the deletion request for the person. */
export function DetailRevokedNotice({ connection, items, headersOnly }: { connection: Connection; items: Receipt[]; headersOnly: boolean }) {
  const [copy, setCopy] = useState<CopyState>('idle')
  const held = connection.hasRead > 0
  const request = deletionRequest(connection, items, headersOnly)

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
      <Notice tone="neutral" title={`Access ended at ${revokedAt}. ${connection.name} can't read anything new.`} action={action}>
        {held
          ? `Permaily can't recall what ${connection.name} has already read. You can ask its provider to delete it, and we've written the request for you.`
          : `It hadn't read anything, so there's nothing to ask its provider to delete.`}
      </Notice>
      {copy === 'failed' && (
        <label className="mt-3 block">
          <span className="text-small text-ink-2">Couldn't copy to the clipboard. Select the request below and copy it yourself.</span>
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
