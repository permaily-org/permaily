import { ChevronLeft, Pencil } from 'lucide-react'
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { Bar, Button, ButtonLink, Notice, Switch, Tag, TextLink } from '../../components/ui/index.ts'
import { attention } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { Conditions } from './Conditions.tsx'
import { connectionNames, joinNames, ruleSeed, type ConditionBlock, type RuleSeed } from './data.ts'
import { emails, initialDraft, preview, savedDraft, type Draft, type Preview } from './draft.ts'
import { PreviewPane } from './PreviewPane.tsx'
import { UsedBy } from './UsedBy.tsx'

export function RuleEditorPage() {
  const { id = 'new' } = useParams()
  const [params] = useSearchParams()
  const pack = params.get('pack')
  const suggested = params.get('suggested')
  const add = params.get('add')
  const seed = useMemo(() => ruleSeed(id, { pack, suggested, add }), [id, pack, suggested, add])
  if (!seed) return <UnknownRule />
  return <RuleEditor key={`${id}:${pack ?? ''}:${suggested ?? ''}:${add ?? ''}`} ruleId={id} seed={seed} />
}

function packChipSet(seed: RuleSeed): Set<string> | null {
  if (!seed.pack) return null
  const unsaved = new Set(seed.unsaved)
  return new Set(seed.blocks.flatMap((block) => block.flatMap((g) => g.chips.filter((c) => !unsaved.has(c)).map((c) => c.toLowerCase()))))
}

function ownChips(blocks: ConditionBlock[], pack: Set<string> | null): string[] {
  if (!pack) return []
  return blocks.flatMap((block) => block.flatMap((g) => g.chips.filter((c) => !pack.has(c.toLowerCase()))))
}

function RuleEditor({ ruleId, seed }: { ruleId: string; seed: RuleSeed }) {
  const navigate = useNavigate()
  const isNew = ruleId === 'new'
  const [draft, setDraft] = useState<Draft>(() => initialDraft(seed))
  const [name, setName] = useState(seed.name)
  const [description, setDescription] = useState(seed.description)
  const [hint, setHint] = useState<string | null>(null)
  const [withhold, setWithhold] = useState(Boolean(seed.withheld))
  const descriptionId = useId()

  const result = useMemo(() => preview(seed, isNew, draft), [seed, isNew, draft])
  const packChips = useMemo(() => packChipSet(seed), [seed])
  const own = ownChips(draft.blocks, packChips)
  const saved = useMemo(() => savedDraft(seed, isNew), [seed, isNew])
  const changed =
    isNew ||
    name !== seed.name ||
    description !== seed.description ||
    withhold !== Boolean(seed.withheld) ||
    JSON.stringify(draft) !== JSON.stringify(saved)
  const hasConditions = draft.blocks.some((block) => block.some((g) => g.chips.length > 0))
  const flag = attention.find((item) => item.to === `/rules/${ruleId}`)
  const readOnly = Boolean(seed.readOnly)

  function rewrite() {
    setHint(description.trim() ? 'The conditions below already cover this sentence.' : 'Write a sentence first, then rewrite the rule from it.')
  }

  function exclude(address: string) {
    setDraft((d) => (d.butNot.includes(address) ? d : { ...d, butNot: [...d.butNot, address] }))
  }

  return (
    <div className="flex min-h-svh">
      <div className="sticky top-0 flex h-svh w-[560px] shrink-0 flex-col">
        <div className="min-h-0 flex-1 overflow-y-auto px-10 pt-7 pb-8">
          <BackToRules />

          <div className="mt-3 flex items-center justify-between gap-4">
            <RuleTitle name={name} readOnly={readOnly} onRename={setName} />
            <div className="flex shrink-0 items-center gap-2">
              {withhold ? (
                <>
                  <Bar width={14} height={14} />
                  <span className="text-small text-ink-2">Withheld from all</span>
                </>
              ) : seed.usedBy.length > 0 ? (
                <>
                  <span className="text-small text-ink-2">Used by</span>
                  <UsedBy initials={seed.usedBy} note={seed.usedByNote} />
                </>
              ) : (
                <span className="text-small text-ink-2">{seed.usedByNote}</span>
              )}
            </div>
          </div>

          {flag && (
            <div className="mt-4">
              <Notice title={flag.title}>{flag.detail}</Notice>
            </div>
          )}

          <label htmlFor={descriptionId} className="mt-3.5 block text-small text-ink-2">
            Describe it in your own words
          </label>
          <div
            className={cx(
              'mt-1.5 flex items-center gap-2 rounded-sm border py-1 pr-1 pl-3 has-[input:focus-visible]:outline-2 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-ink',
              readOnly ? 'border-rule' : 'border-ink',
            )}
          >
            <input
              id={descriptionId}
              value={description}
              readOnly={readOnly}
              onChange={(event) => {
                setDescription(event.target.value)
                setHint(null)
              }}
              placeholder="For example, receipts from shops I buy from"
              className="min-w-0 flex-1 bg-transparent py-1.5 text-body outline-none placeholder:text-ink-3"
            />
            {!readOnly && (
              <Button variant="secondary" size="sm" onClick={rewrite}>
                Rewrite the rule
              </Button>
            )}
          </div>
          <p aria-live="polite" className="mt-1.5 font-mono text-micro text-ink-2">
            {hint ?? 'Only this sentence goes to the model. Your mail does not.'}
          </p>

          {!readOnly && (
            <div className="mt-3 flex items-center justify-between gap-4 border-y border-rule py-2.5">
              <div className="flex min-w-0 flex-col">
                <span className="text-body">Withhold from every connection</span>
                <span className="text-small text-ink-2">Wins over every other rule, including access you approve later.</span>
              </div>
              <Switch checked={withhold} onChange={setWithhold} label="Withhold from every connection" />
            </div>
          )}

          <div className="mt-3">
            <Conditions
              blocks={draft.blocks}
              butNot={draft.butNot}
              packChips={packChips}
              readOnly={readOnly}
              onBlocksChange={(blocks) => setDraft((d) => ({ ...d, blocks }))}
              onButNotChange={(butNot) => setDraft((d) => ({ ...d, butNot }))}
            />
          </div>

          <div className="mt-3.5 flex flex-wrap items-center gap-2 text-small text-ink-2">
            {seed.pack ? (
              <>
                <span>Based on</span>
                <Tag>{seed.pack}</Tag>
                {own.length > 0 && <span>plus {joinNames(own)}, outlined above</span>}
              </>
            ) : (
              <span>{seed.basedOnText}</span>
            )}
          </div>
        </div>

        <SaveBar
          seed={seed}
          isNew={isNew}
          withhold={withhold}
          result={result}
          changed={changed}
          canSave={changed && hasConditions}
          onLeave={() => navigate('/rules')}
        />
      </div>

      <PreviewPane
        preview={result}
        readOnly={readOnly}
        withheld={withhold}
        onExclude={exclude}
        onInclude={(entry) => setDraft((d) => ({ ...d, butNot: d.butNot.filter((e) => e !== entry) }))}
      />
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

function RuleTitle({ name, readOnly, onRename }: { name: string; readOnly: boolean; onRename: (name: string) => void }) {
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
      {!readOnly && (
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
      )}
    </div>
  )
}

function SaveBar({
  seed,
  isNew,
  withhold,
  result,
  changed,
  canSave,
  onLeave,
}: {
  seed: RuleSeed
  isNew: boolean
  withhold: boolean
  result: Preview
  changed: boolean
  canSave: boolean
  onLeave: () => void
}) {
  const bar = 'shrink-0 border-t border-rule-strong px-10 pt-4 pb-3.5'

  if (seed.readOnly) {
    return (
      <div className={bar}>
        <p className="text-small text-ink-2">Built-in rules can't be edited. Rules withheld from all still apply on top of them.</p>
        <div className="mt-2.5">
          <ButtonLink to="/rules" variant="secondary">
            Back to rules
          </ButtonLink>
        </div>
      </div>
    )
  }

  const users = seed.usedBy.length
  const names = joinNames(connectionNames(seed.usedBy))
  const n = result.newCount
  let notice: ReactNode = null
  let saveLabel = 'Save rule'

  const count = result.count
  const wasWithheld = Boolean(seed.withheld)

  if (withhold && isNew) {
    notice = (
      <Notice tone="neutral" title={`Saving withholds ${emails(count)} from every connection.`}>
        That includes connections you approve later. You can change or turn off this rule at any time.
      </Notice>
    )
  } else if (withhold && !wasWithheld && users > 0) {
    saveLabel = 'Save and withhold'
    notice = (
      <Notice title={`${names} ${users === 1 ? 'stops' : 'stop'} getting the ${emails(count)} this rule matches.`}>
        A withheld rule wins over every connection, including the ones that use it.
      </Notice>
    )
  } else if (withhold && n > 0) {
    notice = (
      <Notice tone="neutral" title={`This edit withholds ${emails(n)} more.`}>
        No connection gets them, whatever its own rules match.
      </Notice>
    )
  } else if (withhold && result.dropped > 0) {
    notice = (
      <Notice title={`${emails(result.dropped)} stop being withheld.`}>Connections whose rules match them can read them again.</Notice>
    )
  } else if (withhold && changed) {
    notice = <Notice tone="neutral" title={wasWithheld ? 'This edit withholds nothing new.' : `Saving withholds ${emails(count)} from every connection.`} />
  } else if (withhold) {
    notice = <p className="text-small text-ink-2">Withheld from every connection, including ones you approve later.</p>
  } else if (wasWithheld) {
    notice = (
      <Notice title={`${emails(count)} stop being withheld.`}>
        Connections whose rules match them can read them again. Nothing is shared until a connection uses this rule.
      </Notice>
    )
  } else if (users > 0 && n > 0) {
    saveLabel = 'Save and review access'
    notice = (
      <Notice title={`This edit shares ${n.toLocaleString('en-US')} more ${n === 1 ? 'email' : 'emails'}.`}>
        {names} {users === 1 ? 'uses this rule, so it stays' : 'use this rule, so they stay'} on the old version until you approve.
      </Notice>
    )
  } else if (users > 0 && changed) {
    notice = (
      <Notice tone="neutral" title="This edit shares nothing new.">
        {names} {users === 1 ? 'gets' : 'get'} the new version as soon as you save.
      </Notice>
    )
  } else if (users === 0) {
    notice = <p className="text-small text-ink-2">No connection uses this rule yet, so saving it shares nothing.</p>
  }

  return (
    <div className={bar}>
      {notice}
      <div className={cx('flex gap-3', notice !== null && 'mt-2.5')}>
        <Button onClick={onLeave} disabled={!canSave}>
          {saveLabel}
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
    </div>
  )
}
