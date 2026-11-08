import type { Ref } from 'react'
import { Button, ButtonLink, SectionHeading, TextAction, TextLink } from '../../components/ui/index.ts'
import type { AttentionItem } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'

const PACK_UPDATE = 'pack-update'

/** Things waiting on the person: decisions first, then suggestions. The section takes focus after a decision removes an item. */
export function AttentionList({
  ref,
  items,
  onOpenPackUpdate,
}: {
  ref?: Ref<HTMLElement>
  items: AttentionItem[]
  onOpenPackUpdate: () => void
}) {
  const decisions = items.filter((item) => item.kind === 'decision')
  const suggestions = items.filter((item) => item.kind === 'suggestion')
  const title = items.length === 0 ? 'Nothing needs you' : `${items.length} ${items.length === 1 ? 'thing needs' : 'things need'} you`

  return (
    <section ref={ref} tabIndex={-1} aria-labelledby="attention-heading" className="outline-none">
      <SectionHeading id="attention-heading" title={title} />
      {items.length === 0 && <p className="py-3 text-body text-ink-2">Every connection is working within what you approved.</p>}
      <Group label="Waiting for your decision" items={decisions} onOpenPackUpdate={onOpenPackUpdate} />
      <Group label="Worth a look" items={suggestions} onOpenPackUpdate={onOpenPackUpdate} />
    </section>
  )
}

function Group({ label, items, onOpenPackUpdate }: { label: string; items: AttentionItem[]; onOpenPackUpdate: () => void }) {
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
            <Action item={item} onOpenPackUpdate={onOpenPackUpdate} />
          </li>
        ))}
      </ul>
    </div>
  )
}

function Action({ item, onOpenPackUpdate }: { item: AttentionItem; onOpenPackUpdate: () => void }) {
  const describedBy = `${item.id}-title`
  if (item.to === PACK_UPDATE) {
    const props = { 'aria-haspopup': 'dialog' as const, 'aria-describedby': describedBy, onClick: onOpenPackUpdate }
    return item.kind === 'decision' ? (
      <Button variant="secondary" size="sm" {...props}>
        {item.action}
      </Button>
    ) : (
      <TextAction {...props}>{item.action}</TextAction>
    )
  }
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
