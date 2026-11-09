import { useId } from 'react'
import { ButtonLink, TextAction } from '../../components/ui/index.ts'
import { formatCount, suggestedRule } from '../../data/sample.ts'
import { suggestedRulePath } from './data.ts'

/** The rule Permaily suggests on a new account: sign-in and security mail, held back from every connection. */
export function SuggestedRule({ onDismiss }: { onDismiss?: () => void }) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="border-y border-rule-strong py-4">
      <p className="font-mono text-mono text-ink-2">Suggested rule</p>
      <h2 id={id} className="mt-1 text-title font-medium">
        Hold back {suggestedRule.name.toLowerCase()} mail from every connection
      </h2>
      <p className="mt-0.5 text-small text-ink-2">{suggestedRule.why}</p>
      <ul className="mt-3 border-t border-rule">
        {suggestedRule.covers.map((item) => (
          <li
            key={item.title}
            className="flex items-center justify-between gap-4 border-b border-rule py-2"
          >
            <span className="text-body">{item.title}</span>
            <span className="font-mono text-mono text-ink-2">{formatCount(item.count)} emails</span>
          </li>
        ))}
      </ul>
      <div className="mt-3.5 flex flex-wrap items-center gap-4">
        <ButtonLink to={suggestedRulePath} variant="secondary" size="sm">
          Review the rule
        </ButtonLink>
        {onDismiss && <TextAction onClick={onDismiss}>Not now</TextAction>}
      </div>
    </section>
  )
}
