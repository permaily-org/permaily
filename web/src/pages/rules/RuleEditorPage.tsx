import { ChevronLeft, Pencil } from 'lucide-react'
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { Button, ButtonLink, Checkbox, EffectMark, Notice, TextLink, ViewSwitch } from '../../components/ui/index.ts'
import { appliesToText, attention, connections, type Rule, type RuleEffect } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { Conditions } from './Conditions.tsx'
import { connectionName, joinNames, ruleSeed, type RuleSeed } from './data.ts'
import { emails, initialDraft, preview, savedDraft, type Draft, type Preview } from './draft.ts'
import { PreviewPane } from './PreviewPane.tsx'

export function RuleEditorPage() {
  const { id = 'new' } = useParams()
  const [params] = useSearchParams()
  const suggested = params.get('suggested')
  const add = params.get('add')
  const seed = useMemo(() => ruleSeed(id, { suggested, add }), [id, suggested, add])
  if (!seed) return <UnknownRule />
  return <RuleEditor key={`${id}:${suggested ?? ''}:${add ?? ''}`} ruleId={id} seed={seed} />
}

type Scope = 'all' | 'some'

function RuleEditor({ ruleId, seed }: { ruleId: string; seed: RuleSeed }) {
  const navigate = useNavigate()
  const isNew = ruleId === 'new'
  const [draft, setDraft] = useState<Draft>(() => initialDraft(seed))
  const [name, setName] = useState(seed.name)
  const [description, setDescription] = useState(seed.description)
  const [hint, setHint] = useState<string | null>(null)
  const [effect, setEffect] = useState<RuleEffect>(seed.effect)
  const [removes, setRemoves] = useState(seed.removes)
  const [scope, setScope] = useState<Scope>(seed.appliesTo === 'all' ? 'all' : 'some')
  const [picked, setPicked] = useState<string[]>(seed.appliesTo === 'all' ? [] : seed.appliesTo)
  const descriptionId = useId()
  const removesId = useId()

  const appliesTo: Rule['appliesTo'] = scope === 'all' ? 'all' : connections.filter((c) => picked.includes(c.id)).map((c) => c.id)
  const result = useMemo(() => preview(seed, isNew, draft), [seed, isNew, draft])
  const saved = useMemo(() => savedDraft(seed, isNew), [seed, isNew])
  const changed =
    isNew ||
    name !== seed.name ||
    description !== seed.description ||
    effect !== seed.effect ||
    removes !== seed.removes ||
    JSON.stringify(appliesTo) !== JSON.stringify(seed.appliesTo) ||
    JSON.stringify(draft) !== JSON.stringify(saved)
  const hasConditions = draft.blocks.some((block) => block.some((g) => g.chips.length > 0))
  const complete = hasConditions && (scope === 'all' || picked.length > 0) && (effect === 'hold' || removes.trim() !== '')
  const flag = attention.find((item) => item.to.split('?')[0] === `/rules/${ruleId}`)

  function rewrite() {
    setHint(description.trim() ? 'The conditions below already cover this sentence.' : 'Write a sentence first, then rewrite the rule from it.')
  }

  function exclude(address: string) {
    setDraft((d) => (d.butNot.includes(address) ? d : { ...d, butNot: [...d.butNot, address] }))
  }

  function toggle(id: string) {
    setPicked((list) => (list.includes(id) ? list.filter((p) => p !== id) : [...list, id]))
  }

  return (
    <div className="flex min-h-svh">
      <div className="sticky top-0 flex h-svh w-[560px] shrink-0 flex-col">
        <div className="min-h-0 flex-1 overflow-y-auto px-10 pt-7 pb-8">
          <BackToRules />

          <div className="mt-3 flex items-center justify-between gap-4">
            <RuleTitle name={name} onRename={setName} />
            <span className="flex shrink-0 items-center gap-2 text-small text-ink-2">
              <EffectMark effect={effect} />
              {effect === 'hold' ? 'Holds back' : 'Removes details'}
            </span>
          </div>

          {flag && (
            <div className="mt-4">
              <Notice title={flag.title}>{flag.detail}</Notice>
            </div>
          )}

          <label htmlFor={descriptionId} className="mt-3.5 block text-small text-ink-2">
            Describe it in your own words
          </label>
          <div className="mt-1.5 flex items-center gap-2 rounded-sm border border-ink py-1 pr-1 pl-3 has-[input:focus-visible]:outline-2 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-ink">
            <input
              id={descriptionId}
              value={description}
              onChange={(event) => {
                setDescription(event.target.value)
                setHint(null)
              }}
              placeholder="For example, never share my children's names"
              className="min-w-0 flex-1 bg-transparent py-1.5 text-body outline-none placeholder:text-ink-3"
            />
            <Button variant="secondary" size="sm" onClick={rewrite}>
              Rewrite the rule
            </Button>
          </div>
          <p aria-live="polite" className="mt-1.5 font-mono text-micro text-ink-2">
            {hint ?? 'Only this sentence goes to the model. Your mail does not.'}
          </p>

          <div className="mt-3 border-y border-rule">
            <Setting
              label="What happens"
              hint={effect === 'hold' ? 'Connections never get matching emails.' : 'Only the matching details are taken out. The rest of each email goes through.'}
            >
              <ViewSwitch
                label="What happens"
                value={effect}
                onChange={setEffect}
                options={[
                  { value: 'hold', label: 'Hold back the email' },
                  { value: 'remove', label: 'Remove details' },
                ]}
              />
              {effect === 'remove' && (
                <div className="flex w-full items-center gap-2">
                  <label htmlFor={removesId} className="shrink-0 text-small text-ink-2">
                    Takes out
                  </label>
                  <input
                    id={removesId}
                    value={removes}
                    onChange={(event) => setRemoves(event.target.value)}
                    placeholder="For example, the address"
                    className={cx('min-w-0 flex-1 rounded-sm border border-ink bg-transparent px-2 py-1 text-body placeholder:text-ink-3', focusRing)}
                  />
                </div>
              )}
            </Setting>
            <Setting
              label="Applies to"
              hint={
                scope === 'all'
                  ? 'Including connections you approve later. None of them are told about this rule.'
                  : 'Only the connections you tick. They aren’t told about this rule.'
              }
            >
              <ViewSwitch
                label="Applies to"
                value={scope}
                onChange={setScope}
                options={[
                  { value: 'all', label: 'Every connection' },
                  { value: 'some', label: 'Only some' },
                ]}
              />
              {scope === 'some' && (
                <div role="group" aria-label="Connections this rule applies to" className="flex flex-wrap gap-x-5 gap-y-2">
                  {connections.map((c) => (
                    <Checkbox key={c.id} label={c.name} checked={picked.includes(c.id)} onChange={() => toggle(c.id)} />
                  ))}
                </div>
              )}
            </Setting>
          </div>

          <div className="mt-3">
            <Conditions
              blocks={draft.blocks}
              butNot={draft.butNot}
              onBlocksChange={(blocks) => setDraft((d) => ({ ...d, blocks }))}
              onButNotChange={(butNot) => setDraft((d) => ({ ...d, butNot }))}
            />
          </div>

          <p className="mt-3.5 text-small text-ink-2">{seed.basedOnText}</p>
        </div>

        <SaveBar
          seed={seed}
          isNew={isNew}
          effect={effect}
          removes={removes}
          appliesTo={appliesTo}
          result={result}
          changed={changed}
          canSave={changed && complete}
          onLeave={() => navigate('/rules')}
        />
      </div>

      <PreviewPane
        preview={result}
        effect={effect}
        removes={removes}
        appliesTo={appliesTo}
        onExclude={exclude}
        onInclude={(entry) => setDraft((d) => ({ ...d, butNot: d.butNot.filter((e) => e !== entry) }))}
      />
    </div>
  )
}

function Setting({ label, hint, children }: { label: string; hint: string; children: ReactNode }) {
  return (
    <div className="flex items-start gap-3 border-b border-rule py-3 last:border-b-0">
      <span className="w-[98px] shrink-0 pt-1.5 text-small">{label}</span>
      <div className="flex min-w-0 flex-1 flex-col items-start gap-2.5">
        {children}
        <p className="text-small text-ink-2">{hint}</p>
      </div>
    </div>
  )
}

function BackToRules() {
  return (
    <div className="flex items-center gap-1.5">
      <ChevronLeft aria-hidden size={14} className="text-ink-2" />
      <TextLink to="/rules">Rules</TextLink>
    </div>
  )
}

function RuleTitle({ name, onRename }: { name: string; onRename: (name: string) => void }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(name)
  const returnFocus = useRef(false)
  const button = useRef<HTMLButtonElement>(null)
  const done = useRef(false)

  useEffect(() => {
    if (editing || !returnFocus.current) return
    returnFocus.current = false
    button.current?.focus()
  }, [editing])

  function finish(save: boolean) {
    if (done.current) return
    done.current = true
    if (save && value.trim()) onRename(value.trim())
    returnFocus.current = true
    setEditing(false)
  }

  if (editing) {
    return (
      <h1 className="min-w-0 flex-1">
        <input
          autoFocus
          aria-label="Rule name"
          value={value}
          onChange={(event) => setValue(event.target.value)}
          onBlur={() => finish(true)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') finish(true)
            if (event.key === 'Escape') finish(false)
          }}
          className={cx('w-full rounded-sm border-b border-ink bg-transparent text-display-s font-medium', focusRing)}
        />
      </h1>
    )
  }

  return (
    <div className="flex min-w-0 items-center gap-2">
      <h1 className="min-w-0 text-display-s font-medium break-words">{name}</h1>
      <button
        ref={button}
        type="button"
        aria-label="Rename rule"
        onClick={() => {
          done.current = false
          setValue(name)
          setEditing(true)
        }}
        className={cx('shrink-0 rounded-sm p-1 text-ink-2 hover:text-ink', focusRing)}
      >
        <Pencil aria-hidden size={16} />
      </button>
    </div>
  )
}

/** Connections a change of "Applies to" lets back in, and the ones it newly covers. */
function scopeChange(before: Rule['appliesTo'], after: Rule['appliesTo']) {
  const all = connections.map((c) => c.id)
  const was = before === 'all' ? all : before
  const now = after === 'all' ? all : after
  return {
    freed: was.filter((id) => !now.includes(id)).map(connectionName),
    added: now.filter((id) => !was.includes(id)).map(connectionName),
  }
}

function SaveBar({
  seed,
  isNew,
  effect,
  removes,
  appliesTo,
  result,
  changed,
  canSave,
  onLeave,
}: {
  seed: RuleSeed
  isNew: boolean
  effect: RuleEffect
  removes: string
  appliesTo: Rule['appliesTo']
  result: Preview
  changed: boolean
  canSave: boolean
  onLeave: () => void
}) {
  const bar = 'shrink-0 border-t border-rule-strong px-10 pt-4 pb-3.5'
  const { count, newCount: n, dropped } = result
  const target = appliesTo === 'all' ? 'every connection' : appliesToText(appliesTo)
  const what = removes.trim() || 'the matching details'
  const { freed, added } = scopeChange(seed.appliesTo, appliesTo)
  let notice: ReactNode

  if (isNew) {
    notice =
      effect === 'hold' ? (
        <Notice tone="neutral" title={`Saving holds back ${emails(count)} from ${target}.`}>
          {appliesTo === 'all' ? 'That includes connections you approve later. None of them are told.' : appliesTo.length === 1 ? "It isn't told." : "They aren't told."}
        </Notice>
      ) : (
        <Notice tone="neutral" title={`Saving takes ${what} out of ${emails(count)}.`}>
          The rest of each email still goes to {appliesTo === 'all' ? 'connections whose queries ask for it' : target}.
        </Notice>
      )
  } else if (effect !== seed.effect) {
    notice =
      effect === 'hold' ? (
        <Notice tone="neutral" title={`Saving holds back all ${emails(count)}, not just ${seed.removes}.`} />
      ) : (
        <Notice title={`These ${emails(count)} go through again, with ${what} taken out.`}>Connections whose queries ask for them can get them.</Notice>
      )
  } else if (freed.length > 0) {
    notice = (
      <Notice title={`${joinNames(freed)} can get ${effect === 'hold' ? `these ${emails(count)}` : `${what} in these ${emails(count)}`} again.`}>
        Only if {freed.length === 1 ? 'its query asks' : 'their queries ask'} for them.
      </Notice>
    )
  } else if (added.length > 0) {
    notice = (
      <Notice tone="neutral" title={effect === 'hold' ? `${joinNames(added)} ${added.length === 1 ? 'stops' : 'stop'} getting these ${emails(count)}.` : `${joinNames(added)} ${added.length === 1 ? 'gets' : 'get'} these ${emails(count)} without ${what}.`}>
        {added.length === 1 ? "It isn't told." : "They aren't told."}
      </Notice>
    )
  } else if (n > 0) {
    notice = (
      <Notice tone="neutral" title={effect === 'hold' ? `This edit holds back ${emails(n)} more.` : `This edit takes ${what} out of ${emails(n)} more.`}>
        Connections aren't told.
      </Notice>
    )
  } else if (dropped > 0) {
    notice = (
      <Notice title={effect === 'hold' ? `${emails(dropped)} stop being held back.` : `${emails(dropped)} go through untouched again.`}>
        Connections whose queries ask for them can get them again.
      </Notice>
    )
  } else if (changed) {
    notice = <Notice tone="neutral" title="This edit changes nothing your connections can get." />
  } else {
    notice = (
      <p className="text-small text-ink-2">
        {effect === 'hold'
          ? `Holds back matching emails from ${appliesTo === 'all' ? 'every connection, including ones you approve later' : target}.`
          : `Takes ${what} out before ${appliesTo === 'all' ? 'any connection' : target} gets them.`}
      </p>
    )
  }

  return (
    <div className={bar}>
      {notice}
      <div className="mt-2.5 flex gap-3">
        <Button onClick={onLeave} disabled={!canSave}>
          Save rule
        </Button>
        <Button variant="secondary" onClick={onLeave}>
          Discard
        </Button>
      </div>
    </div>
  )
}

function UnknownRule() {
  return (
    <div className="px-10 pt-7 pb-16">
      <BackToRules />
      <h1 className="mt-3 text-display-s font-medium">There's no rule with that name.</h1>
      <p className="mt-2 text-lead text-ink-2">It may have been deleted. Your other rules are on the Rules page.</p>
      <div className="mt-6">
        <ButtonLink to="/rules" variant="secondary">
          Back to rules
        </ButtonLink>
      </div>
    </div>
  )
}
