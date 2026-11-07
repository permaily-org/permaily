import type { Section } from '../sections.ts'

export function SectionPage({ section }: { section: Section }) {
  return (
    <header className="flex flex-col gap-2.5">
      <h1 className="text-display font-medium">{section.label}</h1>
      <p className="text-lead text-ink-2">{section.purpose}</p>
    </header>
  )
}
