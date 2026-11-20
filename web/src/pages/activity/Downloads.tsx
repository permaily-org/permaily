import { type RefObject, useEffect, useRef, useState } from 'react'
import { Button, InfoTip, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import {
  csvSource,
  type ExportState,
  type ExportStopCode,
  type ExportStopped,
  jsonSource,
  localDay,
  type SavedFile,
  saveFile,
  type VerifiedOf,
  writeExport,
} from '../../lib/csv.ts'
import { useSession } from '../../lib/session.tsx'
import { messageFor } from '../auth/messages.ts'
import { count } from './words.ts'

/*
  The record's two downloads (ADR 0041, "The CSV export"): "for checking", the JSON export that permaily-verify
  -receipts reads, and "as a spreadsheet", the CSV for people. Each is read a page at a time and saved only whole: one
  file while the record fits 16 MiB, else numbered parts and a manifest written last. A set without its manifest says
  it is incomplete, and can go on from the next part while the page is open. Nothing is stored: what the download
  saved and where it stopped live in this component's memory. Words are a first draft for the designer's review.
*/

type Ext = 'csv' | 'jsonl'

type Run =
  | { phase: 'idle' }
  | { phase: 'running'; ext: Ext; receipts: number; parts: number }
  | { phase: 'done'; ext: Ext; files: SavedFile[]; manifest: string | null }
  | { phase: 'stopped'; ext: Ext; state: ExportState; code: ExportStopCode | null }

const what: Record<Ext, string> = { jsonl: 'for checking', csv: 'as a spreadsheet' }

/** The owner the image's list verified for a receipt's vendor, from the connections listed now. */
function verifiedFrom(connections: ReadonlyMap<string, api.Connection>): VerifiedOf {
  return (receipt) => {
    const listed =
      (receipt.connection && connections.get(receipt.connection)) ||
      [...connections.values()].find((c) => c.vendor.domain === receipt.vendor)
    return listed && listed.vendor.domain === receipt.vendor ? listed.vendor.verified : undefined
  }
}

function plural(n: number, one: string, many: string): string {
  return `${count(n)} ${n === 1 ? one : many}`
}

/** Why a download stopped, in fixed words. */
function stoppedWords(code: Exclude<ExportStopCode, 'cancelled'>): string {
  if (code === 'changed')
    return 'Your record changed where the download stopped, so it can’t go on from there. Start again to get your whole record.'
  if (code === 'busy') return 'Your record is busy. Try again in a moment.'
  if (code === 'network') return "Permaily can't be reached."
  return messageFor(code)
}

export function Downloads({
  connections,
  partBytes,
}: {
  connections: ReadonlyMap<string, api.Connection>
  /** The limit of a part: `EXPORT_PART_BYTES` but in tests. */
  partBytes?: number
}) {
  const { failed } = useSession()
  const [run, setRun] = useState<Run>({ phase: 'idle' })
  const cancel = useRef<AbortController | null>(null)
  const startRefs = { jsonl: useRef<HTMLButtonElement>(null), csv: useRef<HTMLButtonElement>(null) }
  const cancelRef = useRef<HTMLButtonElement>(null)
  const focusCancel = useRef(false)
  const live = useRef(true)
  useEffect(() => {
    live.current = true
    return () => {
      live.current = false
      cancel.current?.abort()
    }
  }, [])

  // A download started from a button that goes away (Continue, Start again) puts focus on Cancel.
  useEffect(() => {
    if (run.phase === 'running' && focusCancel.current) {
      focusCancel.current = false
      cancelRef.current?.focus()
    }
  }, [run.phase])

  async function start(ext: Ext, resume?: ExportState, fromGone = false) {
    if (cancel.current) return
    const controller = new AbortController()
    cancel.current = controller
    focusCancel.current = fromGone
    setRun({
      phase: 'running',
      ext,
      receipts: resume?.receipts ?? 0,
      parts: resume?.parts.length ?? 0,
    })
    try {
      const done = await writeExport({
        ext,
        source: ext === 'csv' ? csvSource(verifiedFrom(connections)) : jsonSource,
        save: saveFile,
        day: resume?.day ?? localDay(),
        signal: controller.signal,
        ...(resume ? { resume } : {}),
        ...(partBytes ? { partBytes } : {}),
        onProgress: ({ receipts, parts }) => {
          if (live.current) setRun({ phase: 'running', ext, receipts, parts })
        },
      })
      if (live.current) setRun({ phase: 'done', ext, ...done })
    } catch (error) {
      if (!live.current) return
      const stopped = error as ExportStopped
      const code =
        stopped.code === 'cancelled' || stopped.code === 'changed'
          ? stopped.code
          : failed(new api.ApiError(stopped.code))
      setRun({ phase: 'stopped', ext, state: stopped.state, code })
    } finally {
      cancel.current = null
    }
  }

  function stopIt(ext: Ext) {
    cancel.current?.abort()
    startRefs[ext].current?.focus()
  }

  const running = run.phase === 'running'
  return (
    <section aria-label="Download your record" className="mt-3 flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        {(['jsonl', 'csv'] as const).map((ext) => (
          <Button
            key={ext}
            ref={startRefs[ext]}
            variant="secondary"
            size="sm"
            aria-disabled={running}
            onClick={() => void start(ext)}
          >
            Download {what[ext]}
          </Button>
        ))}
        <InfoTip label="About the downloads" align="start">
          For checking is the file permaily-verify checks against the newest link you noted. As a
          spreadsheet is the same record for reading; it proves nothing. A record over 16 MiB is
          saved in numbered parts, then a list of them.
        </InfoTip>
      </div>
      <Progress run={run} cancelRef={cancelRef} onCancel={stopIt} />
      {run.phase === 'stopped' && (
        <Stopped
          run={run}
          onContinue={() => void start(run.ext, run.state, true)}
          onAgain={() => void start(run.ext, undefined, true)}
        />
      )}
    </section>
  )
}

/** What a download is doing, or what it saved: a polite status, with Cancel while it reads. */
function Progress({
  run,
  cancelRef,
  onCancel,
}: {
  run: Run
  cancelRef: RefObject<HTMLButtonElement | null>
  onCancel: (ext: Ext) => void
}) {
  let words = ''
  if (run.phase === 'running') {
    words = `Downloading ${what[run.ext]}: ${plural(run.receipts, 'receipt', 'receipts')} read`
    words += run.parts > 0 ? `, ${plural(run.parts, 'part', 'parts')} saved.` : '.'
  } else if (run.phase === 'done') {
    words =
      run.manifest === null
        ? `Saved ${run.files[0]?.name}.`
        : `Saved ${plural(run.files.length, 'part', 'parts')} and the list of them, ${run.manifest}.`
  } else if (run.phase === 'stopped' && run.code === 'cancelled' && run.state.parts.length === 0)
    words = 'Download cancelled. Nothing was saved.'
  return (
    <div className="flex flex-col items-start gap-1.5">
      <p role="status" className="text-small text-ink-2">
        {words}
      </p>
      {run.phase === 'running' && run.parts > 0 && (
        <p className="text-small text-ink-2">
          Your browser may ask whether Permaily can save several files. Allow it to get every part.
        </p>
      )}
      {run.phase === 'running' && (
        <TextAction ref={cancelRef} size="sm" onClick={() => onCancel(run.ext)}>
          Cancel <span className="sr-only">the download</span>
        </TextAction>
      )}
    </div>
  )
}

/** A download that stopped: what it saved is not the whole record, and it can go on or start again. */
function Stopped({
  run,
  onContinue,
  onAgain,
}: {
  run: Extract<Run, { phase: 'stopped' }>
  onContinue: () => void
  onAgain: () => void
}) {
  const saved = run.state.parts.length
  if (saved === 0 && run.code === 'cancelled') return null
  const why = run.code !== null && run.code !== 'cancelled' ? stoppedWords(run.code) : null
  const retry = run.code !== 'receipts_unreadable'
  // A set that can't prove where it stopped can only start again.
  const goOn = saved > 0 && run.code !== 'changed'
  return (
    <div role="alert" className="flex flex-col items-start gap-1.5 text-small">
      {saved > 0 ? (
        <p>
          Saved {plural(saved, 'part', 'parts')}. The download stopped before the end: these files
          are not your whole record.
        </p>
      ) : (
        <p>The download stopped. Nothing was saved.</p>
      )}
      {why && <p className="text-danger">{why}</p>}
      {retry && (
        <div className="flex flex-wrap gap-3">
          {goOn && <TextAction onClick={onContinue}>Continue</TextAction>}
          <TextAction onClick={onAgain}>{saved > 0 ? 'Start again' : 'Try again'}</TextAction>
        </div>
      )}
    </div>
  )
}
