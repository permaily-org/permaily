import { useState } from 'react'
import { useSearchParams } from 'react-router'
import { Button, ButtonLink, Headline, SearchField, TextAction, TextLink } from '../../components/ui/index.ts'
import { formatCount, packCategories, packs, type Pack } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { categoryPrefixes, packPrefix, packSources, type PackCategory } from './data.ts'
import { ProposeDialog } from './PackDialogs.tsx'
import { PackPanel } from './PackPanel.tsx'
import { RulesTabs } from './RulesTabs.tsx'

function inCategory(pack: Pack, category: PackCategory): boolean {
  return category === 'All' || categoryPrefixes[category].includes(packPrefix(pack.id))
}

function matchesQuery(pack: Pack, query: string): boolean {
  const q = query.trim().toLowerCase()
  return !q || pack.id.toLowerCase().includes(q) || pack.description.toLowerCase().includes(q)
}

export function PacksPage() {
  const [params, setParams] = useSearchParams()
  const [query, setQuery] = useState('')
  const [category, setCategory] = useState<PackCategory>('All')
  const [selectedId, setSelectedId] = useState(params.get('pack') ?? 'travel.bookings')

  if (params.get('state') === 'error') return <LoadError onRetry={() => setParams({})} />

  const shown = packs.filter((p) => inCategory(p, category) && matchesQuery(p, query))
  const selected = shown.find((p) => p.id === selectedId) ?? shown[0]

  return (
    <div className="flex min-h-svh">
      <div className="min-w-0 flex-1 px-14 pt-8 pb-16">
        <RulesTabs />
        <Headline className="mt-4">Rules written in public.</Headline>
        <p className="mt-2 text-lead text-ink-2">Each pack is a public file. Anyone can propose a change, and nothing reaches you without your approval.</p>

        <div className="mt-5 flex flex-wrap items-center gap-2">
          <SearchField
            label="Search packs"
            placeholder="Search packs"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="w-[220px]"
          />
          <div role="group" aria-label="Category" className="flex flex-wrap gap-2">
            {packCategories.map((c) => (
              <button
                key={c}
                type="button"
                aria-pressed={c === category}
                onClick={() => setCategory(c)}
                className={cx(
                  'rounded-sm border px-2.5 py-[5px] text-small',
                  c === category ? 'border-ink bg-ink text-paper' : 'border-rule text-ink hover:border-ink',
                  focusRing,
                )}
              >
                {c}
              </button>
            ))}
          </div>
        </div>

        {shown.length > 0 ? (
          <ul aria-label="Community packs" className="mt-[22px] border-t border-rule-strong">
            {shown.map((pack) => (
              <PackRow key={pack.id} pack={pack} selected={pack.id === selected?.id} onSelect={() => setSelectedId(pack.id)} />
            ))}
          </ul>
        ) : (
          <NoResults query={query.trim()} category={category} />
        )}
      </div>

      <PackPanel pack={selected} />
    </div>
  )
}

function PackRow({ pack, selected, onSelect }: { pack: Pack; selected: boolean; onSelect: () => void }) {
  return (
    <li
      className={cx(
        'relative flex items-center border-b border-l-2 border-rule',
        selected ? 'border-l-ink bg-sunken' : 'border-l-transparent hover:bg-sunken',
        'has-[button:focus-visible]:outline-2 has-[button:focus-visible]:outline-offset-2 has-[button:focus-visible]:outline-ink',
      )}
    >
      <button
        type="button"
        aria-current={selected || undefined}
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-center py-2 pl-1.5 text-left outline-none after:absolute after:inset-0"
      >
        <span className="flex min-w-0 flex-1 flex-col pr-4">
          <span className="font-mono text-small font-medium">{pack.id}</span>
          <span className="text-small text-ink-2">{pack.description}</span>
        </span>
        <span className="w-[132px] shrink-0 font-mono text-mono">{formatCount(pack.yours)} of yours</span>
      </button>
      <span className="w-[122px] shrink-0 pr-2 text-right">
        <PackStatus pack={pack} />
      </span>
    </li>
  )
}

function PackStatus({ pack }: { pack: Pack }) {
  const update = packSources[pack.id]?.update
  if (pack.state === 'update' && update) return <span className="text-small text-attention">Version {update.version} available</span>
  if (pack.state === 'available') {
    return (
      <TextLink to={`/rules/new?pack=${pack.id}`} className="relative z-10" aria-label={`Preview ${pack.id}`}>
        Preview
      </TextLink>
    )
  }
  return <span className="text-small text-ink-2">In use</span>
}

function NoResults({ query, category }: { query: string; category: PackCategory }) {
  const [proposing, setProposing] = useState(false)
  return (
    <div className="mt-[22px] border-t border-rule-strong pt-8">
      <h2 className="text-heading font-medium">{query ? `No pack matches "${query}".` : `No ${category.toLowerCase()} packs yet.`}</h2>
      <p className="mt-2 max-w-[480px] text-body text-ink-2">You can write your own rule now, and propose it as a pack if others would use it.</p>
      <div className="mt-5">
        <ButtonLink to="/rules/new">Write a rule</ButtonLink>
      </div>
      <div className="mt-4">
        <TextAction onClick={() => setProposing(true)}>Propose a new pack</TextAction>
      </div>
      {proposing && (
        <ProposeDialog
          title="Propose a new pack"
          fieldLabel="What should it match?"
          placeholder="Padel club bookings"
          initial={query}
          sentText={(idea) => `Sent. If maintainers take on "${idea}", it shows up here as a pack anyone can use.`}
          onClose={() => setProposing(false)}
        />
      )}
    </div>
  )
}

function LoadError({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="px-14 pt-8 pb-16">
      <RulesTabs />
      <div className="mt-4 max-w-[720px]">
        <Headline>We couldn't load the pack list.</Headline>
        <p className="mt-2 text-lead text-ink-2">Your rules keep working. The versions you already use are stored with your account.</p>
        <div className="mt-5">
          <Button variant="secondary" onClick={onRetry}>
            Try again
          </Button>
        </div>
      </div>
    </div>
  )
}
