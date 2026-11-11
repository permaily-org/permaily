import { ChevronLeft, Pencil } from 'lucide-react'
import { type ReactNode, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import {
  Button,
  ButtonLink,
  EffectMark,
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
  connectionsOf,
  everyConnection,
  type RuleEffect,
  vendors,
} from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { Criteria } from './Criteria.tsx'
import {
  type Criterion,
  operators,
  type Removal,
  type RuleSeed,
  removalComplete,
  ruleSeed,
} from './data.ts'
import { type Draft, initialDraft, preview, savedDraft } from './draft.ts'
import { PreviewPane } from './PreviewPane.tsx'
import { RemovalParts } from './Removal.tsx'
import { TargetSearch } from './Targets.tsx'

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

/**
 * The editor lists what a rule applies to, and none means every connection. A saved "every connection except" becomes
 * the vendors and connections it leaves in.
 */
function targetsOf(appliesTo: AppliesTo): string[] {
  if (appliesTo.to === 'some') return appliesTo.only
  if (appliesTo.except.length === 0) return []
  return vendors.flatMap((vendor) => {
    const list = connectionsOf(vendor.id)
    const kept = list.filter((c) => !appliesTo.except.some((t) => t === vendor.id || t === c.id))
    return kept.length === list.length ? [vendor.id] : kept.map((c) => c.id)
  })
}

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
  const [removal, setRemoval] = useState<Removal>(seed.removal)
  const initialTargets = useMemo(() => targetsOf(seed.appliesTo), [seed])
  const [targets, setTargets] = useState<string[]>(initialTargets)
  const descriptionId = useId()

  // Targets left as they were keep the rule's own wording, such as "every connection except", when it's saved.
  const sameTargets = JSON.stringify(targets) === JSON.stringify(initialTargets)
  const appliesTo: AppliesTo = sameTargets
    ? seed.appliesTo
    : targets.length === 0
      ? everyConnection
      : { to: 'some', only: targets }
  const result = useMemo(() => preview(seed, isNew, draft), [seed, isNew, draft])
  const saved = useMemo(() => savedDraft(seed, isNew), [seed, isNew])
  const changed =
    isNew ||
    name !== seed.name ||
    description !== seed.description ||
    effect !== seed.effect ||
    JSON.stringify(removal) !== JSON.stringify(seed.removal) ||
    !sameTargets ||
    JSON.stringify(draft) !== JSON.stringify(saved)
  const hasConditions = draft.groups.some((g) => g.criteria.some((c) => c.values.length > 0))
  const complete = hasConditions && (effect === 'hold' || removalComplete(removal))
  const flag = attention.find((item) => item.to.split('?')[0] === `/rules/${ruleId}`)

  function rewrite() {
    setHint(
      description.trim()
        ? 'The conditions below already cover this sentence.'
        : 'Write a sentence first, then rewrite the rule from it.',
    )
  }

  /** Excluding a sender from the preview leaves it out of every group, adding "Sender isn't" where a group has none. */
  function exclude(address: string) {
    setDraft((d) => ({
      groups: d.groups.map((g) => {
        const notFrom = g.criteria.find((c) => c.field === 'sender' && c.operator === 'isNot')
        if (notFrom?.values.includes(address)) return g
        const criteria: Criterion[] = notFrom
          ? g.criteria.map((c) => (c === notFrom ? { ...c, values: [...c.values, address] } : c))
          : [
              ...g.criteria,
              { id: `${g.id}-not-from`, field: 'sender', operator: 'isNot', values: [address] },
            ]
        return { ...g, criteria }
      }),
    }))
  }

  /** Including it again takes it out of every negative condition, and drops a condition or group left empty. */
  function include(entry: string) {
    setDraft((d) => ({
      groups: d.groups
        .map((g) => ({
          ...g,
          criteria: g.criteria.flatMap((c) => {
            if (!operators[c.operator].negative || !c.values.includes(entry)) return [c]
            const values = c.values.filter((v) => v !== entry)
            return values.length > 0 ? [{ ...c, values }] : []
          }),
        }))
        .filter((g) => g.criteria.length > 0),
    }))
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
            <Setting
              label="What happens"
              tip="Full holds back the whole email. Partial takes out only the parts you pick, and the rest still goes through."
            >
              <ViewSwitch
                label="What happens"
                value={effect}
                onChange={setEffect}
                options={[
                  { value: 'hold', label: <EffectLabel effect="hold">Full</EffectLabel> },
                  { value: 'remove', label: <EffectLabel effect="remove">Partial</EffectLabel> },
                ]}
              />
              {effect === 'remove' && <RemovalParts removal={removal} onChange={setRemoval} />}
            </Setting>
            <Setting
              label="Applies to"
              tip="With none picked, it applies to every connection, including ones you approve later. A vendor covers all of its connections. None of them are told about this rule."
            >
              <TargetSearch targets={targets} onChange={setTargets} />
            </Setting>
          </div>

          <div className="mt-3">
            <Criteria groups={draft.groups} onChange={(groups) => setDraft({ groups })} />
          </div>
        </div>

        <SaveBar
          canSave={changed && complete}
          onSave={() => {
            if (!isNew) setRuleScopes({ [ruleId]: appliesTo })
            navigate(returnTo)
          }}
          onDiscard={() => navigate(returnTo)}
        />
      </div>

      <PreviewPane preview={result} effect={effect} onExclude={exclude} onInclude={include} />
    </div>
  )
}

/** An effect with its mark. The mark sits on a paper tile, so it keeps its colors when the option is picked and turns dark. */
function EffectLabel({ effect, children }: { effect: RuleEffect; children: ReactNode }) {
  return (
    // A flex row rather than an inline one: the mark has no text, so on a line of text it would sit off the baseline.
    <span className="flex items-center gap-2">
      <span className="inline-flex size-[18px] shrink-0 items-center justify-center rounded-[1px] bg-paper">
        <EffectMark effect={effect} />
      </span>
      {children}
    </span>
  )
}

/** A label beside its controls, or above them on a phone, where the controls need the whole width. */
function Setting({ label, tip, children }: { label: string; tip?: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 border-b border-rule py-3 last:border-b-0 sm:flex-row sm:items-start sm:gap-3">
      <span className="flex items-center gap-1.5 text-small whitespace-nowrap sm:w-[116px] sm:shrink-0 sm:pt-1.5">
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

/**
 * Save and Discard. Below xl the bar sticks to the foot of the window until the editor column ends, and the page's scroll
 * padding keeps a field that gets focus from scrolling in behind it.
 */
function SaveBar({
  canSave,
  onSave,
  onDiscard,
}: {
  canSave: boolean
  onSave: () => void
  onDiscard: () => void
}) {
  return (
    <div className="flex shrink-0 gap-3 border-t border-rule-strong px-4 pt-4 pb-3.5 sm:px-8 lg:px-10 max-xl:sticky max-xl:bottom-0 max-xl:bg-paper max-xl:[html:has(&)]:scroll-pb-24">
      <Button onClick={onSave} disabled={!canSave}>
        Save rule
      </Button>
      <Button variant="secondary" onClick={onDiscard}>
        Discard
      </Button>
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
