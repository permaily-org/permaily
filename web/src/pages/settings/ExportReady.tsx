import { useMemo } from 'react'
import { Button } from '../../components/ui/index.ts'
import { connections, neverShare, receipts, rules } from '../../data/sample.ts'
import { downloadText } from '../activity/data.ts'

const filename = 'permaily-export.json'

function formatSize(bytes: number): string {
  return bytes < 1024 * 1024 ? `${Math.max(1, Math.round(bytes / 1024))} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

/** The export ready state, shown in place under Your data. */
export function ExportReady() {
  const json = useMemo(
    () =>
      JSON.stringify(
        {
          rules,
          access: connections.map(({ id, name, kind, ruleNames, parts, actions, time, ends }) => ({ id, name, kind, ruleNames, parts, actions, time, ends })),
          neverShare: { defaults: neverShare.defaults, mine: neverShare.mine },
          receipts,
        },
        null,
        2,
      ),
    [],
  )
  const size = formatSize(new Blob([json]).size)

  return (
    <div role="status" className="border-b border-rule pt-5 pb-6">
      <h3 className="text-title font-medium">Your export is ready.</h3>
      <p className="mt-1.5 text-small text-ink-2">It holds your rules, access settings and receipts. It doesn't include email.</p>
      <div className="mt-3.5 flex items-center justify-between gap-4 border-y border-rule py-2.5 font-mono text-mono">
        <span>{filename}</span>
        <span className="text-ink-2">{size}, link ends in 24 h</span>
      </div>
      <Button className="mt-4" onClick={() => downloadText(filename, json, 'application/json')}>
        Download
      </Button>
    </div>
  )
}
