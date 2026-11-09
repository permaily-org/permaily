import { useMemo } from 'react'
import { Button } from '../../components/ui/index.ts'
import { useRules } from '../../data/ruleScopes.ts'
import { connections, receipts, vendors } from '../../data/sample.ts'
import { downloadText } from '../activity/data.ts'

const filename = 'permaily-export.json'

function formatSize(bytes: number): string {
  return bytes < 1024 * 1024
    ? `${Math.max(1, Math.round(bytes / 1024))} KB`
    : `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

/** The export ready state, shown in place under Your data. */
export function ExportReady() {
  const rules = useRules()
  const json = useMemo(
    () =>
      JSON.stringify(
        {
          rules,
          vendors,
          connections: connections.map(
            ({
              id,
              vendorId,
              name,
              asksFor,
              query,
              trigger,
              webhook,
              parts,
              actions,
              time,
              ends,
            }) => ({
              id,
              vendorId,
              name,
              asksFor,
              query,
              trigger,
              webhook: webhook?.url,
              parts,
              actions,
              time,
              ends,
            }),
          ),
          receipts,
        },
        null,
        2,
      ),
    [rules],
  )
  const size = formatSize(new Blob([json]).size)

  return (
    <div role="status" className="border-b border-rule pt-5 pb-6">
      <h3 className="text-title font-medium">Your export is ready.</h3>
      <div className="mt-3.5 flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-y border-rule py-2.5 font-mono text-mono">
        <span>{filename}</span>
        <span className="text-ink-2">{size}, link ends in 24 h</span>
      </div>
      <Button className="mt-4" onClick={() => downloadText(filename, json, 'application/json')}>
        Download
      </Button>
    </div>
  )
}
