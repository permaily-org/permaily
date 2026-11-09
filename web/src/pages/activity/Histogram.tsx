import { dailyCounts, spike } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'

const maxHeight = 58

function sum(list: number[]): number {
  return list.reduce((total, n) => total + n, 0)
}

/** Emails that left per day for two weeks. Last week is pale as a baseline, this week bright, and the spike in attention. */
export function Histogram() {
  const max = Math.max(...dailyCounts)
  const lastWeek = sum(dailyCounts.slice(0, 7))
  const thisWeek = sum(dailyCounts.slice(7))
  const label = `Emails that left per day over two weeks: ${lastWeek} last week, ${thisWeek} this week. ${spike.label}.`

  return (
    <figure className="mt-5">
      <div
        role="img"
        aria-label={label}
        className="flex items-end gap-1"
        style={{ height: maxHeight }}
      >
        {dailyCounts.map((count, i) => (
          <span
            key={i}
            className={cx(
              'flex-1',
              i === spike.index ? 'bg-attention' : i < 7 ? 'bg-highlight-soft' : 'bg-highlight',
            )}
            style={{ height: Math.max(2, Math.round((count / max) * maxHeight)) }}
          />
        ))}
      </div>
      <figcaption
        aria-hidden
        className="mt-1.5 grid grid-cols-2 sm:grid-cols-[1fr_auto_1fr] font-mono text-micro text-ink-2"
      >
        <span>last week</span>
        <span className="col-span-2 row-start-2 mt-0.5 text-attention sm:col-span-1 sm:row-start-auto sm:mt-0">
          {spike.label}
        </span>
        <span className="text-right">this week</span>
      </figcaption>
    </figure>
  )
}
