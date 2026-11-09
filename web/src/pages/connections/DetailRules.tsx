import { useId, useState } from 'react'
import { Link } from 'react-router'
import {
  Button,
  Checkbox,
  Dialog,
  EffectMark,
  SectionHeading,
  TextAction,
  TextLink,
} from '../../components/ui/index.ts'
import { setRuleScopes, useRules } from '../../data/ruleScopes.ts'
import {
  appliesToText,
  type Connection,
  connectionLabel,
  effectText,
  joinNames,
  type Rule,
  ruleCovers,
  withConnection,
} from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'

/**
 * The person's rules that apply to this connection, with a way to change which ones do. A change
 * here changes the rule itself, so the rule editor shows it too. The connection is never told.
 */
export function DetailRules({ connection, revoked }: { connection: Connection; revoked: boolean }) {
  const rules = useRules()
  const [open, setOpen] = useState(false)
  const [session, setSession] = useState(0)
  const applied = rules.filter((rule) => ruleCovers(rule.appliesTo, connection))
  const others = rules.filter((rule) => !ruleCovers(rule.appliesTo, connection))

  const change = revoked ? undefined : (
    <TextAction
      onClick={() => {
        setSession((s) => s + 1)
        setOpen(true)
      }}
      aria-label={`Change which rules apply to ${connection.name}`}
    >
      Change
    </TextAction>
  )

  return (
    <section aria-labelledby="rules-heading">
      <SectionHeading id="rules-heading" title="Rules" action={change} />
      {applied.length === 0 ? (
        <p className="py-[11px] text-body text-ink-2">None apply</p>
      ) : (
        <ul>
          {applied.map((rule) => (
            <li key={rule.id}>
              <Link
                to={`/rules/${rule.id}`}
                className={cx(
                  'flex items-center gap-2.5 border-b border-rule py-[11px] hover:bg-sunken',
                  focusRing,
                )}
              >
                <EffectMark effect={rule.effect} />
                <span className="min-w-0 flex-1 truncate text-body">{rule.name}</span>
                <span className="shrink-0 text-small text-ink-2">{effectText(rule)}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      {others.length > 0 && (
        <p className="pt-2.5 text-small text-ink-2">
          Not applied: {joinNames(others.map((rule) => rule.name))}
        </p>
      )}
      {open && (
        <RulesDialog
          key={session}
          connection={connection}
          rules={rules}
          onClose={() => setOpen(false)}
        />
      )}
    </section>
  )
}

/** Tick the rules that apply to this connection. Each row says who the rule will cover after saving. */
function RulesDialog({
  connection,
  rules,
  onClose,
}: {
  connection: Connection
  rules: Rule[]
  onClose: () => void
}) {
  const titleId = useId()
  const [ticked, setTicked] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(rules.map((r) => [r.id, ruleCovers(r.appliesTo, connection)])),
  )
  const next = (rule: Rule) => withConnection(rule.appliesTo, connection, ticked[rule.id] ?? false)
  const changed = rules.filter((rule) => next(rule) !== rule.appliesTo)

  function save() {
    setRuleScopes(Object.fromEntries(changed.map((rule) => [rule.id, next(rule)])))
    onClose()
  }

  return (
    <Dialog open onClose={onClose} labelledBy={titleId}>
      <h2 id={titleId} className="text-heading font-medium">
        Rules for {connectionLabel(connection)}
      </h2>
      <ul aria-label="Your rules" className="mt-5 border-t border-rule-strong">
        {rules.map((rule) => (
          <li key={rule.id} className="border-b border-rule py-2.5">
            <Checkbox
              className="w-full"
              checked={ticked[rule.id] ?? false}
              onChange={() => setTicked((t) => ({ ...t, [rule.id]: !t[rule.id] }))}
              label={
                <span className="flex items-center gap-3">
                  <EffectMark effect={rule.effect} />
                  <span className="font-medium">{rule.name}</span>
                  <span className="ml-auto max-w-[300px] text-right text-small text-ink-2">
                    {appliesToText(next(rule))}
                  </span>
                </span>
              }
            />
          </li>
        ))}
      </ul>
      <TextLink to={`/rules/new?for=${encodeURIComponent(connection.id)}`} className="mt-4">
        New rule for this connection
      </TextLink>
      <div className="mt-6 flex gap-2.5">
        <Button onClick={save} disabled={changed.length === 0}>
          Save
        </Button>
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </Dialog>
  )
}
