import { ChevronLeft, X } from 'lucide-react'
import { type ReactNode, useEffect, useId, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router'
import { useConfirmed } from '../../components/Confirm.tsx'
import {
  Button,
  Checkbox,
  Dialog,
  Headline,
  Notice,
  TextAction,
  TextLink,
} from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { isCancelled } from '../../lib/cancelled.ts'
import { RULES_NOTICE_VERSION } from '../../lib/rules-notice.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { LoadState } from '../accounts/shared.tsx'
import { messageFor, ruleProblemFor } from '../auth/messages.ts'
import { CriteriaEditor } from './CriteriaEditor.tsx'
import { type Criteria, fromCriteria, toCriteria } from './criteria.ts'
import { RulePreview } from './RulePreview.tsx'
import { ModelOffNotice } from './RulesPage.tsx'
import { noticeAgreed, RuleSentence, RulesNotice, useRuleList } from './shared.tsx'
import {
  confirmationLines,
  inFlight,
  modelDown,
  partWords,
  removableParts,
  ruleName,
  rulesProof,
  senderAndAttachments,
  shapeWords,
  topicsOverRemove,
} from './words.ts'

/** The editor's fields: what the person changes, and the draft they make. */
type Fields = {
  name: string
  criteria: Criteria[]
  effect: api.RuleEffect
  parts: api.ConnectionPart[]
  shapes: api.ShapeKind[]
  topics: string[]
  to: api.RuleScope['to']
  clients: string[]
  on: boolean
}

const blank: Fields = {
  name: '',
  criteria: [],
  effect: 'hold',
  parts: [],
  shapes: [],
  topics: [],
  to: 'all',
  clients: [],
  on: true,
}

/** The editor's fields of a checked draft, its criteria with the server's sentence of each. */
function fieldsOf(check: api.RuleCheck): Fields {
  const d = check.draft
  return {
    name: d.name,
    criteria: fromCriteria(d.criteria, check.sentence.formulas),
    effect: d.effect,
    parts: d.removal?.parts ?? [],
    shapes: d.removal?.shapes ?? [],
    topics: d.removal?.topics ?? [],
    to: d.scope.to,
    clients: d.scope.clients ?? [],
    on: d.on,
  }
}

/** The draft the fields make: what is checked, previewed and saved. */
function draftOf(f: Fields): api.RuleDraft {
  const removal: api.RuleRemoval = {
    ...(f.parts.length ? { parts: removableParts.filter((p) => f.parts.includes(p)) } : {}),
    ...(f.shapes.length ? { shapes: api.SHAPE_KINDS.filter((k) => f.shapes.includes(k)) } : {}),
    ...(f.topics.length ? { topics: f.topics } : {}),
  }
  return {
    name: f.name.trim(),
    criteria: toCriteria(f.criteria),
    effect: f.effect,
    ...(f.effect === 'remove' ? { removal } : {}),
    scope: f.to === 'all' ? { to: 'all' } : { to: f.to, clients: f.clients },
    on: f.on,
  }
}

/**
 * A rule, new (/rules/new) or the person's own (/rules/:id) (ADR 0040, "The screens"): words to a proposal, the
 * criteria, what it does, who for and its switch; then the sentence the server wrote of it, which Save confirms with a
 * fresh proof; a preview on the person's mail; and Delete. Words drafted for the designer's review.
 */
export function RuleEditorPage() {
  const { id = 'new' } = useParams()
  const { list, setList, failure, load } = useRuleList()
  if (list === null) {
    return (
      <div className={pagePadding}>
        <BackToRules />
        <Headline className="mt-3">{id === 'new' ? 'New rule' : 'Rule'}</Headline>
        <div className="mt-6 max-w-[880px]">
          <LoadState failure={failure} what="Your rules" onRetry={() => void load()} />
        </div>
      </div>
    )
  }
  const rule = id === 'new' ? undefined : list.rules.find((r) => r.id === id)
  if (id !== 'new' && !rule) return <UnknownRule />
  return <Editor key={id} rule={rule} list={list} onList={setList} reload={load} />
}

function Editor({
  rule,
  list,
  onList,
  reload,
}: {
  rule: api.Rule | undefined
  list: api.RuleList
  onList: (list: api.RuleList) => void
  reload: () => Promise<api.RuleList | null>
}) {
  const { failed } = useSession()
  const confirmed = useConfirmed()
  const navigate = useNavigate()
  const [fields, setFields] = useState<Fields>(() => (rule ? fieldsOf(rule.check) : blank))
  const [check, setCheck] = useState<{ key: string; check: api.RuleCheck } | null>(null)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [asking, setAsking] = useState<'save' | 'delete' | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [changedElsewhere, setChangedElsewhere] = useState<api.Rule | null>(null)
  const [connections, setConnections] = useState<api.Connection[]>([])
  const [mailboxes, setMailboxes] = useState<api.Mailbox[]>([])
  // A new round asks for the sentence of the same draft again: after the server refused the one checked.
  const [round, setRound] = useState(0)
  const agreed = noticeAgreed(list)
  const draft = useMemo(() => draftOf(fields), [fields])
  const draftKey = JSON.stringify(draft)
  const checked = check?.key === draftKey ? check.check : null
  const full = !rule && list.rules.length >= list.max

  useEffect(() => {
    let live = true
    api.connections().then(
      (answer) => {
        if (live) setConnections(answer.connections)
      },
      () => {},
    )
    api.mailboxes().then(
      (answer) => {
        if (live) setMailboxes(answer.mailboxes)
      },
      () => {},
    )
    return () => {
      live = false
    }
  }, [])

  // The server writes the sentence of every draft the person makes: asked a moment after they stop changing it, and
  // again in a new round.
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new round asks again, though the draft is the same
  useEffect(() => {
    let live = true
    const timer = setTimeout(() => {
      api.checkRule(JSON.parse(draftKey) as api.RuleDraft).then(
        (answer) => {
          if (!live) return
          setCheck({ key: draftKey, check: answer })
          setCheckError(null)
        },
        (problem: unknown) => {
          if (!live) return
          setCheck(null)
          const code = failed(problem)
          if (code) {
            setCheckError(
              ruleProblemFor(code, problem instanceof api.ApiError ? problem.rule : undefined),
            )
          }
        },
      )
    }, 300)
    return () => {
      live = false
      clearTimeout(timer)
    }
  }, [draftKey, failed, round])

  // Agreeing moves the rules' version: read them again, keeping the fields, for the next save to name it.
  async function agreedTo(notice: api.RuleNoticeAgreement) {
    if (!(await reload())) onList({ ...list, notice })
  }

  function set<K extends keyof Fields>(key: K, value: Fields[K]) {
    setFields((f) => ({ ...f, [key]: value }))
  }

  async function save() {
    if (pending || !checked) return
    setPending(true)
    setError(null)
    try {
      const saved = await confirmed(
        () =>
          api.saveRule(
            {
              draft_id: checked.draft_id,
              draft: checked.draft,
              version: list.version,
              ...(checked.model ? { notice: RULES_NOTICE_VERSION } : {}),
            },
            rule?.id,
          ),
        rulesProof,
      )
      setAsking(null)
      navigate('/rules', { state: { said: `${ruleName(saved.check)} saved. ${inFlight}` } })
    } catch (problem) {
      setAsking(null)
      if (isCancelled(problem)) return
      const code = failed(problem)
      if (!code) return
      setError(ruleProblemFor(code, problem instanceof api.ApiError ? problem.rule : undefined))
      if (code === 'rule_changed') {
        // The sentence the person read is no longer the server's: ask for it again, for them to read and save.
        setCheck(null)
        setRound((r) => r + 1)
      }
      if (code === 'rules_changed' || code === 'rules_notice_needed') {
        const next = await reload()
        const now = rule && next?.rules.find((r) => r.id === rule.id)
        if (now && now.check.draft_id !== rule?.check.draft_id) setChangedElsewhere(now)
        if (rule && next && !now) setChangedElsewhere(null)
      }
    } finally {
      setPending(false)
    }
  }

  async function remove() {
    if (pending || !rule) return
    setPending(true)
    setError(null)
    try {
      await confirmed(() => api.deleteRule(rule.id, list.version), rulesProof)
      setAsking(null)
      navigate('/rules', { state: { said: `${ruleName(rule.check)} deleted. ${inFlight}` } })
    } catch (problem) {
      setAsking(null)
      if (isCancelled(problem)) return
      const code = failed(problem)
      if (!code) return
      setError(messageFor(code))
      if (code === 'rules_changed') void reload()
    } finally {
      setPending(false)
    }
  }

  const title = rule ? ruleName(rule.check) : 'New rule'

  return (
    <div className={pagePadding}>
      <BackToRules />
      <h1 className="mt-3 text-display-s font-medium break-words">
        <bdi>{title}</bdi>
      </h1>

      <div className="mt-6 flex max-w-[720px] flex-col gap-8">
        {rule?.model_off && <ModelOffNotice />}
        {changedElsewhere && (
          <Notice title="This rule was changed in another window.">
            Saved now: <RuleSentence sentence={changedElsewhere.check.sentence} />{' '}
            <bdi>{changedElsewhere.check.does}</bdi>. <bdi>{changedElsewhere.check.for}</bdi>.{' '}
            <TextAction
              onClick={() => {
                setFields(fieldsOf(changedElsewhere.check))
                setChangedElsewhere(null)
              }}
            >
              Edit the saved one instead
            </TextAction>
          </Notice>
        )}

        <Words
          agreed={agreed}
          onAgreed={agreedTo}
          onProposal={(proposal) =>
            setFields((f) => {
              const next = fieldsOf(proposal.check)
              // The model is told nothing of the account's connections: who a rule is for stays the person's choice.
              return rule
                ? { ...next, name: f.name || next.name, to: f.to, clients: f.clients, on: f.on }
                : next
            })
          }
        />

        <Field label="Name" htmlFor="rule-name">
          <input
            id="rule-name"
            value={fields.name}
            maxLength={60}
            onChange={(event) => set('name', event.target.value)}
            placeholder="Home address"
            className={cx(
              'w-full max-w-[420px] rounded-sm border border-rule bg-transparent px-2.5 py-1.5 text-body placeholder:text-ink-3 hover:border-ink',
              focusRing,
            )}
          />
        </Field>

        <CriteriaEditor
          criteria={fields.criteria}
          mailboxes={mailboxes}
          onChange={(criteria) => set('criteria', criteria)}
        />

        <Effect fields={fields} onChange={setFields} />

        <Scope fields={fields} connections={connections} check={checked} onChange={setFields} />

        <Checkbox
          label="On"
          checked={fields.on}
          onChange={(event) => set('on', event.target.checked)}
        />

        <section aria-labelledby="saving-heading" className="flex flex-col gap-3">
          <h2 id="saving-heading" className="text-title font-medium">
            What you save
          </h2>
          <div role="status" aria-live="polite" className="flex flex-col gap-1.5">
            {checked ? (
              <Checked check={checked} />
            ) : checkError ? (
              <p className="text-small text-danger">{checkError}</p>
            ) : (
              <p className="text-small text-ink-2">
                Permaily is writing its sentence of this rule.
              </p>
            )}
          </div>
          <p role="alert" className="text-small text-danger empty:hidden">
            {error}
          </p>
          {full && (
            <p className="text-small text-ink-2">
              You have {list.max} rules, as many as an account can. Delete one to make another.
            </p>
          )}
          <div className="flex flex-wrap gap-2.5">
            <Button
              aria-disabled={!checked || full || pending}
              onClick={() => {
                if (checked && !full) setAsking('save')
              }}
            >
              Save rule
            </Button>
            {rule && (
              <Button variant="danger" onClick={() => setAsking('delete')}>
                Delete rule
              </Button>
            )}
          </div>
        </section>

        <RulePreview draft={draft} mailboxes={mailboxes} agreed={agreed} />
      </div>

      <Dialog open={asking === 'save'} onClose={() => setAsking(null)} labelledBy="save-heading">
        <h2
          id="save-heading"
          data-dialog-focus
          tabIndex={-1}
          className="text-title font-medium outline-none"
        >
          Save this rule?
        </h2>
        {checked && (
          <div className="mt-3 flex flex-col gap-2">
            <Checked check={checked} />
            <div className="flex flex-col gap-1.5 text-small text-ink-2">
              {confirmationLines(checked.draft).map((line) => (
                <p key={line}>{line}</p>
              ))}
            </div>
            {checked.model && !agreed && <RulesNotice headingLevel="h3" onAgreed={agreedTo} />}
          </div>
        )}
        <div className="mt-6 flex flex-wrap gap-2.5">
          <Button
            aria-disabled={pending || (checked?.model === true && !agreed)}
            onClick={() => {
              if (!(checked?.model && !agreed)) void save()
            }}
          >
            Save
          </Button>
          <Button variant="secondary" onClick={() => setAsking(null)}>
            Cancel
          </Button>
        </div>
      </Dialog>

      {rule && (
        <Dialog
          open={asking === 'delete'}
          onClose={() => setAsking(null)}
          labelledBy="delete-heading"
        >
          <h2
            id="delete-heading"
            data-dialog-focus
            tabIndex={-1}
            className="text-title font-medium outline-none"
          >
            Delete <bdi>{ruleName(rule.check)}</bdi>?
          </h2>
          <div className="mt-3 flex flex-col gap-2 text-ink-2">
            <p>
              Mail it holds back or takes details out of can then leave to the connections it was
              for.
            </p>
            <p>{inFlight}</p>
          </div>
          <div className="mt-6 flex flex-wrap gap-2.5">
            <Button variant="danger" aria-disabled={pending} onClick={() => void remove()}>
              Delete
            </Button>
            <Button variant="secondary" onClick={() => setAsking(null)}>
              Cancel
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  )
}

/** The server's sentence of a draft: its criteria, what it does and who for, and whether a model decides any of it. */
function Checked({ check }: { check: api.RuleCheck }) {
  return (
    <div className="flex flex-col gap-1">
      <span>
        <RuleSentence sentence={check.sentence} />
      </span>
      <bdi className="text-small">{check.does}</bdi>
      <bdi className="text-small">{check.for}</bdi>
      {check.model && <span className="text-small text-ink-2">{modelDown}</span>}
    </div>
  )
}

/** Describing the rule in words, for the model to propose one: under the rules' notice, agreed above it first. */
function Words({
  agreed,
  onAgreed,
  onProposal,
}: {
  agreed: boolean
  onAgreed: (notice: api.RuleNoticeAgreement) => Promise<void>
  onProposal: (proposal: api.RuleProposal) => void
}) {
  const { failed } = useSession()
  const [words, setWords] = useState('')
  const [pending, setPending] = useState(false)
  const [said, setSaid] = useState<string | null>(null)
  const [down, setDown] = useState(false)
  const wordsId = useId()

  async function turn() {
    const w = words.trim()
    if (pending || !w) return
    setPending(true)
    setSaid(null)
    try {
      onProposal(await api.ruleWords(w, RULES_NOTICE_VERSION))
      setSaid(
        'Proposed: the fields below are the model’s. Read the sentence Permaily wrote of it before you save.',
      )
    } catch (problem) {
      const code = failed(problem)
      if (!code) return
      if (code === 'model_unavailable') setDown(true)
      setSaid(messageFor(code))
    } finally {
      setPending(false)
    }
  }

  return (
    <section aria-labelledby={`${wordsId}-heading`} className="flex flex-col gap-2">
      <h2 id={`${wordsId}-heading`} className="text-title font-medium">
        Describe it in your words
      </h2>
      {!agreed ? (
        <RulesNotice headingLevel="h3" onAgreed={onAgreed} />
      ) : (
        <>
          <label htmlFor={wordsId} className="text-small text-ink-2">
            {down
              ? 'Permaily can’t ask its model now. Build the rule from the conditions below.'
              : 'Only these words go to the model: nothing of your mail or account. They are kept nowhere.'}
          </label>
          <textarea
            id={wordsId}
            value={words}
            maxLength={1000}
            rows={3}
            onChange={(event) => setWords(event.target.value)}
            placeholder="Take my home address out of everything apps read"
            className={cx(
              'w-full rounded-sm border border-ink bg-transparent px-3 py-2 text-body placeholder:text-ink-3',
              focusRing,
            )}
          />
          <div>
            <Button
              variant="secondary"
              size="sm"
              aria-disabled={pending || words.trim() === ''}
              onClick={() => void turn()}
            >
              Turn into a rule
            </Button>
          </div>
        </>
      )}
      <p role="status" className="text-small text-ink-2 empty:hidden">
        {said}
      </p>
    </section>
  )
}

/** What the rule does: holds the email back, or takes details out, each kind with what it takes out besides. */
function Effect({ fields, onChange }: { fields: Fields; onChange: (f: Fields) => void }) {
  const groupId = useId()
  const toggle = <T,>(list: T[], value: T, on: boolean) =>
    on ? [...list, value] : list.filter((v) => v !== value)
  const reads = fields.shapes.length > 0 || fields.topics.length > 0
  return (
    <fieldset className="flex flex-col gap-3">
      <legend className="mb-1.5 text-title font-medium">What it does</legend>
      <div role="radiogroup" aria-labelledby={groupId} className="flex flex-col gap-2">
        <span id={groupId} className="sr-only">
          What it does
        </span>
        <Radio
          name="effect"
          checked={fields.effect === 'hold'}
          onChange={() => onChange({ ...fields, effect: 'hold' })}
          label="Hold the email back: it doesn’t leave, and the app isn’t told it exists"
        />
        <Radio
          name="effect"
          checked={fields.effect === 'remove'}
          onChange={() => onChange({ ...fields, effect: 'remove' })}
          label="Take details out: the rest leaves"
        />
      </div>
      {fields.effect === 'remove' && (
        <div className="flex flex-col gap-4 border-l border-rule pl-4">
          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-1 text-small font-medium">Parts, taken out whole</legend>
            {removableParts.map((part) => (
              <Checkbox
                key={part}
                label={partWords[part]}
                checked={fields.parts.includes(part)}
                onChange={(event) =>
                  onChange({ ...fields, parts: toggle(fields.parts, part, event.target.checked) })
                }
              />
            ))}
          </fieldset>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-small font-medium">
              Kinds of data, found by pattern with no model
            </legend>
            {api.SHAPE_KINDS.map((kind) => (
              <Checkbox
                key={kind}
                top
                label={
                  <span className="flex flex-col">
                    <span>{shapeWords[kind].label}</span>
                    <span className="text-small text-ink-2">{shapeWords[kind].also}</span>
                  </span>
                }
                checked={fields.shapes.includes(kind)}
                onChange={(event) =>
                  onChange({ ...fields, shapes: toggle(fields.shapes, kind, event.target.checked) })
                }
              />
            ))}
          </fieldset>
          <Topics topics={fields.topics} onChange={(topics) => onChange({ ...fields, topics })} />
          {reads && <p className="text-small text-ink-2">{senderAndAttachments}</p>}
        </div>
      )}
    </fieldset>
  )
}

/** Topics a model takes out sentence by sentence, at most three, with what that takes out besides. */
function Topics({ topics, onChange }: { topics: string[]; onChange: (t: string[]) => void }) {
  const [value, setValue] = useState('')
  const inputId = useId()
  function add() {
    const v = value.trim()
    if (!v || topics.includes(v) || topics.length >= 3) return
    onChange([...topics, v])
    setValue('')
  }
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={inputId} className="text-small font-medium">
        Topics, sentence by sentence (a model decides)
      </label>
      <p className="text-small text-ink-2">{topicsOverRemove}</p>
      {topics.length > 0 && (
        <ul className="flex flex-wrap gap-1.5">
          {topics.map((topic) => (
            <li
              key={topic}
              className="inline-flex items-center gap-1 rounded-sm bg-sunken px-2 text-small"
            >
              <bdi>{topic}</bdi>
              <button
                type="button"
                aria-label={`Remove topic ${topic}`}
                onClick={() => onChange(topics.filter((t) => t !== topic))}
                className={cx(
                  'inline-flex size-5 items-center justify-center rounded-sm text-ink-2 hover:bg-rule hover:text-ink',
                  focusRing,
                )}
              >
                <X aria-hidden size={12} strokeWidth={2} />
              </button>
            </li>
          ))}
        </ul>
      )}
      {topics.length < 3 && (
        <span className="flex items-center gap-2">
          <input
            id={inputId}
            value={value}
            maxLength={100}
            placeholder="a home address"
            onChange={(event) => setValue(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                add()
              }
            }}
            className={cx(
              'h-[30px] w-64 rounded-sm border border-rule bg-transparent px-2 text-small placeholder:text-ink-3 hover:border-ink',
              focusRing,
            )}
          />
          <TextAction onClick={add}>
            Add <span className="sr-only">topic</span>
          </TextAction>
        </span>
      )}
    </div>
  )
}

/**
 * Who the rule is for: every connection, every one but the apps picked, or only them. An app is its client, shown by
 * `clientShown` (its host and document path), with the name its own document gives labelled as the app's words.
 */
function Scope({
  fields,
  connections,
  check,
  onChange,
}: {
  fields: Fields
  connections: api.Connection[]
  check: api.RuleCheck | null
  onChange: (f: Fields) => void
}) {
  const apps = new Map<string, string>()
  for (const c of connections) if (!apps.has(c.client_id)) apps.set(c.client_id, c.client_name)
  for (const c of check?.clients ?? [])
    if (!apps.has(c.client_id)) apps.set(c.client_id, c.name ?? '')
  for (const id of fields.clients) if (!apps.has(id)) apps.set(id, '')
  const choose = (to: api.RuleScope['to']) =>
    onChange({ ...fields, to, clients: to === 'all' ? [] : fields.clients })
  return (
    <fieldset className="flex flex-col gap-3">
      <legend className="mb-1.5 text-title font-medium">Who it’s for</legend>
      <div className="flex flex-col gap-2">
        <Radio
          name="scope"
          checked={fields.to === 'all'}
          onChange={() => choose('all')}
          label="Every connection, those you allow later included"
        />
        <Radio
          name="scope"
          checked={fields.to === 'all_except'}
          onChange={() => choose('all_except')}
          label="Every connection but the apps you pick, those you allow later included"
        />
        <Radio
          name="scope"
          checked={fields.to === 'only'}
          onChange={() => choose('only')}
          label="Only the apps you pick"
        />
      </div>
      {fields.to !== 'all' && (
        <fieldset className="flex flex-col gap-2 border-l border-rule pl-4">
          <legend className="mb-1 text-small font-medium">
            {fields.to === 'only' ? 'Only these apps' : 'Not these apps'}
          </legend>
          {apps.size === 0 && (
            <p className="text-small text-ink-2">You have no connections to pick from yet.</p>
          )}
          {[...apps].map(([id, name]) => (
            <Checkbox
              key={id}
              top
              label={
                <span className="flex flex-col">
                  <bdi className="font-mono text-mono break-all">{api.clientShown(id)}</bdi>
                  {name && (
                    <span className="text-small text-ink-2">
                      calls itself <bdi>{name}</bdi>
                    </span>
                  )}
                </span>
              }
              checked={fields.clients.includes(id)}
              onChange={(event) =>
                onChange({
                  ...fields,
                  clients: event.target.checked
                    ? [...fields.clients, id]
                    : fields.clients.filter((c) => c !== id),
                })
              }
            />
          ))}
          <p className="text-small text-ink-2">
            An app is told apart by the address of its document, never by its domain alone: another
            app on the same domain is another app.
          </p>
        </fieldset>
      )}
    </fieldset>
  )
}

function Radio({
  name,
  checked,
  onChange,
  label,
}: {
  name: string
  checked: boolean
  onChange: () => void
  label: ReactNode
}) {
  return (
    <label className="inline-flex items-start gap-2.5">
      <input
        type="radio"
        name={name}
        checked={checked}
        onChange={onChange}
        className={cx('mt-1 size-[16px] shrink-0 accent-ink', focusRing)}
      />
      <span className="min-w-0 flex-1">{label}</span>
    </label>
  )
}

function Field({
  label,
  htmlFor,
  children,
}: {
  label: string
  htmlFor: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className="text-small text-ink-2">
        {label}
      </label>
      {children}
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

function UnknownRule() {
  return (
    <div className={pagePadding}>
      <BackToRules />
      <Headline size="display-s" className="mt-3">
        There's no rule of yours here.
      </Headline>
      <p className="mt-3 text-ink-2">It may have been deleted in another window.</p>
    </div>
  )
}
