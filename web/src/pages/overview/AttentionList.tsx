import { ButtonLink, SectionHeading, TextLink } from '../../components/ui/index.ts'
import type { AttentionItem } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'

/** Things waiting on the person: decisions first, then suggestions. */
export function AttentionList({ items }: { items: AttentionItem[] }) {
  const decisions = items.filter((item) => item.kind === 'decision')
  const suggestions = items.filter((item) => item.kind === 'suggestion')
  const title = items.length === 0 ? 'Nothing needs you' : `${items.length} ${items.length === 1 ? 'thing needs' : 'things need'} you`

  return (
    <section aria-labelledby="attention-heading">
      <SectionHeading id="attention-heading" title={title} />
      {items.length === 0 && <p className="py-3 text-body text-ink-2">Every connection is working within what you approved.</p>}
      <Group label="Waiting for your decision" items={decisions} />
      <Group label="Worth a look" items={suggestions} />
    </section>
  )
}

function Group({ label, items }: { label: string; items: AttentionItem[] }) {
  if (items.length === 0) return null
  return (
    <div>
      <h3 className="pt-2.5 text-small text-ink-2">{label}</h3>
      <ul>
        {items.map((item) => (
          <li
            key={item.id}
            className={cx(
              'flex items-center gap-4 border-b border-rule',
              item.kind === 'decision' ? 'min-h-[46px] py-1.5' : 'min-h-[38px] py-[9px]',
            )}
          >
            <p className="flex min-w-0 flex-1 flex-wrap gap-x-4">
              <span id={`${item.id}-title`} className="font-medium">
                {item.title}
              </span>
              <span className="text-ink-2">{item.detail}</span>
            </p>
            <Action item={item} />
          </li>
        ))}
      </ul>
    </div>
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
