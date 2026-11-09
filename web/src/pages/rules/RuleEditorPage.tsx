import { ChevronLeft, Pencil } from 'lucide-react'
import { type ReactNode, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import {
  Button,
  ButtonLink,
  InfoTip,
  Notice,
  TextLink,
  ViewSwitch,
} from '../../components/ui/index.ts'
import { setRuleScopes, useRuleScopes } from '../../data/ruleScopes.ts'
import {
  type AppliesTo,
  attention,
  connectionById,
  connectionLabel,
  type RuleEffect,
  ruleConnections,
  targetLabel,
} from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { Conditions } from './Conditions.tsx'
import { joinNames, type RuleSeed, ruleSeed } from './data.ts'
import { type Draft, emails, initialDraft, type Preview, preview, savedDraft } from './draft.ts'
import { PreviewPane } from './PreviewPane.tsx'
import { Targets } from './Targets.tsx'

export function RuleEditorPage() {
  const { id = 'new' } = useParams()
  const [params] = useSearchParams()
  const suggested = params.get('suggested')
  const add = params.get('add')
  /** A new rule started from a connection's page applies to that connection, and goes back there. */
  const forConnection = connectionById(params.get('for') ?? undefined)
  const scopes = useRuleScopes()
  const seed = useMemo(() => {
    const base = ruleSeed(id, { suggested, add })
    if (!base) return base
    if (id !== 'new') return { ...base, appliesTo: scopes[id] ?? base.appliesTo }
    return forConnection
      ? { ...base, appliesTo: { to: 'some', only: [forConnection.id] } satisfies AppliesTo }
      : base
  }, [id, suggested, add, forConnection, scopes])
  if (!seed) return <UnknownRule />
  return (
    <RuleEditor
      key={`${id}:${suggested ?? ''}:${add ?? ''}:${forConnection?.id ?? ''}`}
      ruleId={id}
      seed={seed}
      returnTo={forConnection ? `/connections/${forConnection.id}` : '/rules'}
    />
  )
}

type Scope = 'all' | 'some'

function RuleEditor({
  ruleId,
  seed,
  returnTo,
}: {
  ruleId: string
  seed: RuleSeed
  returnTo: string
}) {
  const navigate = useNavigate()
  const isNew = ruleId === 'new'
  const [draft, setDraft] = useState<Draft>(() => initialDraft(seed))
  const [name, setName] = useState(seed.name)
  const [description, setDescription] = useState(seed.description)
  const [hint, setHint] = useState<string | null>(null)
  const [effect, setEffect] = useState<RuleEffect>(seed.effect)
  const [removes, setRemoves] = useState(seed.removes)
  const [scope, setScope] = useState<Scope>(seed.appliesTo.to)
  const [except, setExcept] = useState<string[]>(
    seed.appliesTo.to === 'all' ? seed.appliesTo.except : [],
  )
  const [only, setOnly] = useState<string[]>(
    seed.appliesTo.to === 'some' ? seed.appliesTo.only : [],
  )
  const descriptionId = useId()
  const removesId = useId()

  const appliesTo: AppliesTo = scope === 'all' ? { to: 'all', except } : { to: 'some', only }
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
  const complete =
    hasConditions &&
    (scope === 'all' || only.length > 0) &&
    (effect === 'hold' || removes.trim() !== '')
  const flag = attention.find((item) => item.to.split('?')[0] === `/rules/${ruleId}`)

  function rewrite() {
    setHint(
      description.trim()
        ? 'The conditions below already cover this sentence.'
        : 'Write a sentence first, then rewrite the rule from it.',
    )
  }

  function exclude(address: string) {
    setDraft((d) => (d.butNot.includes(address) ? d : { ...d, butNot: [...d.butNot, address] }))
  }

  // From xl the editor is a column that stays in place with the save bar at its foot, and the preview scrolls beside
  // it. Below xl the page is one column: the editor, then the preview.
  return (
    <div className="flex flex-col xl:min-h-svh xl:flex-row">
      <div className="flex w-full max-w-[560px] shrink-0 flex-col xl:sticky xl:top-0 xl:h-svh xl:w-[560px]">
        <div className="px-4 pt-7 pb-8 sm:px-8 lg:px-10 xl:min-h-0 xl:flex-1 xl:overflow-y-auto">
          <BackToRules />

          <div className="mt-3">
            <RuleTitle name={name} onRename={setName} />
          </div>

          {flag && (
            <div className="mt-4">
              <Notice title={flag.title}>{flag.detail}</Notice>
            </div>
          )}

          <div className="mt-3.5 flex items-center gap-1.5">
            <label htmlFor={descriptionId} className="text-small text-ink-2">
              Describe it in your own words
            </label>
            <InfoTip label="What the model sees">
              Only this sentence goes to the model. Your mail doesn't.
            </InfoTip>
          </div>
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
          <p aria-live="polite" className="mt-1.5 font-mono text-micro text-ink-2 empty:hidden">
            {hint}
          </p>

          <div className="mt-3 border-y border-rule">
            <Setting label="What happens">
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
                    className={cx(
                      'min-w-0 flex-1 rounded-sm border border-ink bg-transparent px-2 py-1 text-body placeholder:text-ink-3',
                      focusRing,
                    )}
                  />
                </div>
              )}
            </Setting>
            <Setting
              label="Applies to"
              tip="Every connection includes ones you approve later. A vendor covers all of its connections. None of them are told about this rule."
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
              <div className="flex w-full items-start gap-3">
                <span className="w-12 shrink-0 py-0.5 text-small text-ink-2">
                  {scope === 'all' ? 'Except' : 'Only'}
                </span>
                {scope === 'all' ? (
                  <Targets key="except" label="Except" targets={except} onChange={setExcept} />
                ) : (
                  <Targets key="only" label="Only" targets={only} onChange={setOnly} />
                )}
              </div>
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
          onSave={() => {
            if (!isNew) setRuleScopes({ [ruleId]: appliesTo })
            navigate(returnTo)
          }}
          onDiscard={() => navigate(returnTo)}
        />
      </div>

      <PreviewPane
        preview={result}
        effect={effect}
        onExclude={exclude}
        onInclude={(entry) =>
          setDraft((d) => ({ ...d, butNot: d.butNot.filter((e) => e !== entry) }))
        }
      />
    </div>
  )
}

/** A label beside its controls, or above them on a phone, where the controls need the whole width. */
function Setting({ label, tip, children }: { label: string; tip?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 border-b border-rule py-3 last:border-b-0 sm:flex-row sm:items-start sm:gap-3">
      <span className="flex items-center gap-1.5 text-small sm:w-[98px] sm:shrink-0 sm:pt-1.5">
        {label}
        {tip && (
          <InfoTip label={`About ${label.toLowerCase()}`} align="start">
            {tip}
          </InfoTip>
        )}
      </span>
      <div className="flex min-w-0 flex-1 flex-col items-start gap-2.5">{children}</div>
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
          className={cx(
            'w-full rounded-sm border-b border-ink bg-transparent text-display-s font-medium',
            focusRing,
          )}
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
function scopeChange(before: AppliesTo, after: AppliesTo) {
  const was = ruleConnections(before)
  const now = ruleConnections(after)
  return {
    freed: was.filter((c) => !now.includes(c)).map(connectionLabel),
    added: now.filter((c) => !was.includes(c)).map(connectionLabel),
  }
}

/** "every connection", "every connection except Claude · Morning summary", or "Claude". */
function targetText(appliesTo: AppliesTo): string {
  if (appliesTo.to === 'some') return joinNames(appliesTo.only.map(targetLabel))
  const all = 'every connection'
  return appliesTo.except.length === 0
    ? all
    : `${all} except ${joinNames(appliesTo.except.map(targetLabel))}`
}

/** What saving would change, in one line, above the buttons. */
function SaveBar({
  seed,
  isNew,
  effect,
  removes,
  appliesTo,
  result,
  changed,
  canSave,
  onSave,
  onDiscard,
}: {
  seed: RuleSeed
  isNew: boolean
  effect: RuleEffect
  removes: string
  appliesTo: AppliesTo
  result: Preview
  changed: boolean
  canSave: boolean
  onSave: () => void
  onDiscard: () => void
}) {
  const { count, newCount: n, dropped } = result
  const what = removes.trim() || 'the matching details'
  const { freed, added } = scopeChange(seed.appliesTo, appliesTo)
  let notice: ReactNode = null

  if (isNew) {
    notice = (
      <Notice
        tone="neutral"
        title={
          effect === 'hold'
            ? `Saving holds back ${emails(count)} from ${targetText(appliesTo)}.`
            : `Saving takes ${what} out of ${emails(count)}.`
        }
      />
    )
  } else if (effect !== seed.effect) {
    notice =
      effect === 'hold' ? (
        <Notice
          tone="neutral"
          title={`Saving holds back all ${emails(count)}, not just ${seed.removes}.`}
        />
      ) : (
        <Notice title={`These ${emails(count)} go through again, with ${what} taken out.`} />
      )
  } else if (freed.length > 0) {
    notice = (
      <Notice
        title={`${joinNames(freed)} can get ${effect === 'hold' ? `these ${emails(count)}` : `${what} in these ${emails(count)}`} again.`}
      />
    )
  } else if (added.length > 0) {
    notice = (
      <Notice
        tone="neutral"
        title={
          effect === 'hold'
            ? `${joinNames(added)} ${added.length === 1 ? 'stops' : 'stop'} getting these ${emails(count)}.`
            : `${joinNames(added)} ${added.length === 1 ? 'gets' : 'get'} these ${emails(count)} without ${what}.`
        }
      />
    )
  } else if (n > 0) {
    notice = (
      <Notice
        tone="neutral"
        title={
          effect === 'hold'
            ? `This edit holds back ${emails(n)} more.`
            : `This edit takes ${what} out of ${emails(n)} more.`
        }
      />
    )
  } else if (dropped > 0) {
    notice = (
      <Notice
        title={
          effect === 'hold'
            ? `${emails(dropped)} stop being held back.`
            : `${emails(dropped)} go through untouched again.`
        }
      />
    )
  } else if (changed) {
    notice = <Notice tone="neutral" title="This edit changes nothing your connections can get." />
  }

  // Below xl the bar sticks to the foot of the window until the editor column ends, and the page's scroll padding keeps
  // a field that gets focus from scrolling in behind it.
  return (
    <div className="shrink-0 border-t border-rule-strong px-4 pt-4 pb-3.5 sm:px-8 lg:px-10 max-xl:sticky max-xl:bottom-0 max-xl:bg-paper max-xl:[html:has(&)]:scroll-pb-40">
      {notice}
      <div className={cx('flex gap-3', notice && 'mt-2.5')}>
        <Button onClick={onSave} disabled={!canSave}>
          Save rule
        </Button>
        <Button variant="secondary" onClick={onDiscard}>
          Discard
        </Button>
      </div>
    </div>
  )
}

function UnknownRule() {
  return (
    <div className="px-4 pt-7 pb-16 sm:px-8 lg:px-10">
      <BackToRules />
      <h1 className="mt-3 text-display-s font-medium">There's no rule with that name.</h1>
      <div className="mt-6">
        <ButtonLink to="/rules" variant="secondary">
          Back to rules
        </ButtonLink>
      </div>
    </div>
  )
}
