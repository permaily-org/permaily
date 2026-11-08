import { useState, type ReactNode } from 'react'
import { ButtonLink, Notice, TextAction } from '../../components/ui/index.ts'
import { formatCount, type Pack } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'
import { listWithMore, packSources, ruleForPack, type PackSource } from './data.ts'
import { ProposeDialog, SourceDialog } from './PackDialogs.tsx'

const panel = 'sticky top-0 flex h-svh w-[400px] shrink-0 flex-col overflow-y-auto border-l border-rule bg-raised px-7 pt-8 pb-8'

export function PackPanel({ pack }: { pack: Pack | undefined }) {
  const source = pack && packSources[pack.id]
  if (!pack || !source) {
    return (
      <aside aria-label="Pack details" className={panel}>
        <p className="text-small text-ink-2">Clear the search to see a pack here.</p>
      </aside>
    )
  }
  return <PackDetails key={pack.id} pack={pack} source={source} />
}

function PackDetails({ pack, source }: { pack: Pack; source: PackSource }) {
  const [dialog, setDialog] = useState<'source' | 'propose' | null>(null)
  const close = () => setDialog(null)

  return (
    <aside aria-label={`${pack.id} details`} className={panel}>
      <p className="font-mono text-mono text-ink-2">
        {pack.id}, version {source.version}
      </p>
      <h2 className="mt-1.5 text-heading font-medium">{pack.description}</h2>
      <p className="mt-5 flex items-center gap-2">
        <span className="bg-highlight px-1.5 py-px text-heading font-medium text-on-highlight">{formatCount(pack.yours)}</span>
        <span className="text-body text-ink-2">of your emails match</span>
      </p>

      <PanelSection title="What it matches">
        {source.data && <PanelRow label={source.data.label} mono>{listWithMore(source.data.machine, 2)}</PanelRow>}
        {source.senders && <PanelRow label="Senders" mono>{listWithMore(source.senders)}</PanelRow>}
        {source.subject && <PanelRow label="Subject has" mono>{source.subject.join(', ')}</PanelRow>}
      </PanelSection>

      <PanelSection title="Who checked it">
        <PanelRow label="Reviewed by">{source.reviewedBy} maintainers</PanelRow>
        <PanelRow label="Contributors">{source.contributors} people</PanelRow>
        <PanelRow label="Checks">{source.checks}</PanelRow>
      </PanelSection>

      {source.update && (
        <div className="mt-5">
          <Notice title={`Version ${source.update.version} adds ${source.update.adds.length} senders`}>
            <span className="font-mono text-mono">{source.update.adds.join(', ')}</span>
          </Notice>
        </div>
      )}

      <div className="min-h-8 flex-1" />

      <div className="flex gap-4">
        <TextAction onClick={() => setDialog('source')}>View source</TextAction>
        <TextAction onClick={() => setDialog('propose')}>Propose a sender</TextAction>
      </div>
      <div className="mt-5">
        <PrimaryAction pack={pack} source={source} />
      </div>

      {dialog === 'source' && <SourceDialog pack={pack} source={source} onClose={close} />}
      {dialog === 'propose' && (
        <ProposeDialog
          title={`Propose a sender for ${pack.id}`}
          fieldLabel="Sender domain"
          placeholder="example.com"
          sentText={(domain) => `Sent. The maintainers of ${pack.id} will review ${domain}. If they add it, it reaches you as a new version for you to approve.`}
          onClose={close}
        />
      )}
    </aside>
  )
}

function PrimaryAction({ pack, source }: { pack: Pack; source: PackSource }) {
  const rule = ruleForPack(pack.id)
  if (source.update && rule) return <ButtonLink to={`/rules/${rule.id}`}>Review version {source.update.version}</ButtonLink>
  if (pack.state === 'in-use' && rule) return <ButtonLink to={`/rules/${rule.id}`}>Open {rule.name}</ButtonLink>
  return <ButtonLink to={`/rules/new?pack=${pack.id}`}>Preview</ButtonLink>
}

function PanelSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="mt-5">
      <h3 className="border-b border-rule-strong pb-1.5 text-body font-medium">{title}</h3>
      <dl>{children}</dl>
    </section>
  )
}

function PanelRow({ label, mono, children }: { label: string; mono?: boolean; children: ReactNode }) {
  return (
    <div className="flex gap-3 border-b border-rule py-2">
      <dt className="w-24 shrink-0 text-small text-ink-2">{label}</dt>
      <dd className={cx('min-w-0 flex-1', mono ? 'font-mono text-mono' : 'text-small')}>{children}</dd>
    </div>
  )
}
