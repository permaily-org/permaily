import { Button, Headline } from '../../components/ui/index.ts'
import { formatCount, inbox } from '../../data/sample.ts'
import { PreviewEyebrow } from './InboxPreview.tsx'

const skeleton: Array<[number, number, number]> = [
  [44, 96, 300],
  [44, 96, 200],
  [44, 96, 340],
  [44, 96, 240],
  [44, 96, 380],
  [44, 96, 260],
  [44, 96, 320],
  [44, 96, 220],
]

/** The preview while the search runs. Gray, not black, so it never reads as withheld mail. */
export function LoadingPreview() {
  const total = formatCount(inbox.total)
  return (
    <div role="status">
      <p className="font-mono text-mono">Checking {total} emails inside the verified service</p>
      <Headline as="h2" size="display-l" className="mt-2 text-ink-3">
        <span aria-hidden className="inline-block h-[0.9em] w-[1.6em] bg-rule motion-safe:animate-pulse" />
        <span>of {total} emails</span>
      </Headline>
      <div aria-hidden className="mt-4 border-t border-rule-strong">
        {skeleton.map(([a, b, c], i) => (
          <div key={i} className="flex h-10 items-center gap-4 border-b border-rule">
            <span className="flex w-16 shrink-0">
              <span className="block h-3 bg-rule" style={{ width: a }} />
            </span>
            <span className="flex w-[124px] shrink-0">
              <span className="block h-3 bg-rule" style={{ width: b }} />
            </span>
            <span className="block h-3 max-w-full bg-rule" style={{ width: c }} />
          </div>
        ))}
      </div>
      <p className="mt-4 text-small text-ink-2">Nothing is shared while this runs.</p>
    </div>
  )
}

export function ErrorPreview({ onRetry }: { onRetry: () => void }) {
  return (
    <div>
      <PreviewEyebrow>Live preview, run on your own mail</PreviewEyebrow>
      <Headline as="h2" size="display" className="mt-2">
        We couldn't run the preview.
      </Headline>
      <p className="mt-3 max-w-[560px] text-ink-2">Nothing has been shared. You won't be asked to approve something you can't see.</p>
      <Button className="mt-5" onClick={onRetry}>
        Try again
      </Button>
      <p className="mt-4 font-mono text-mono text-ink-2">gmail api timed out after 30 s, ref e_4c91</p>
    </div>
  )
}
