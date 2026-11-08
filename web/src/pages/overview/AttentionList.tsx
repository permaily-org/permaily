import { ButtonLink, SectionHeading, TextLink } from '../../components/ui/index.ts'
import type { AttentionItem } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'

/** Things waiting on the person: decisions first, with a button, then suggestions. */
export function AttentionList({ items }: { items: AttentionItem[] }) {
  const sorted = [...items.filter((item) => item.kind === 'decision'), ...items.filter((item) => item.kind === 'suggestion')]
  const title = items.length === 0 ? 'Nothing needs you' : `${items.length} ${items.length === 1 ? 'thing needs' : 'things need'} you`

  return (
    <section aria-labelledby="attention-heading">
      <SectionHeading id="attention-heading" title={title} />
      <ul>
        {sorted.map((item) => (
          <li
            key={item.id}
            className={cx('flex items-center gap-4 border-b border-rule', item.kind === 'decision' ? 'min-h-[46px] py-1.5' : 'min-h-[40px] py-[9px]')}
          >
            <p className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3">
              <span id={`${item.id}-title`} className="font-medium">
                {item.title}
              </span>
              {item.detail && <span className="font-mono text-mono text-ink-2">{item.detail}</span>}
            </p>
            <Action item={item} />
          </li>
        ))}
      </ul>
    </section>
  )
}

function Action({ item }: { item: AttentionItem }) {
  const describedBy = `${item.id}-title`
  if (item.kind === 'decision') {
    return (
      <ButtonLink to={item.to} variant="secondary" size="sm" aria-describedby={describedBy}>
        {item.action}
      </ButtonLink>
    )
  }
  return (
    <TextLink to={item.to} aria-describedby={describedBy}>
      {item.action}
    </TextLink>
  )
}
