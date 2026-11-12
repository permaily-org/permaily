// A whole message from its blocks, as the preview shows it. The service never runs this: it fills the
// templates that scripts/build.ts writes from the same components.

import {
  Action,
  type Block,
  Code,
  FactRow,
  Facts,
  Layout,
  Lead,
  MachineValue,
  Note,
  Paragraph,
} from './blocks.tsx'

/** A message as the service writes it: what internal/services/accounts writes into fixtures/. */
export type Message = { name: string; to: string; subject: string; blocks: Block[] }

export function BlockView({ block }: { block: Block }) {
  switch (block.kind) {
    case 'lead':
      return <Lead text={block.text} />
    case 'paragraph':
      return <Paragraph text={block.text} />
    case 'note':
      return <Note text={block.text} />
    case 'code':
      return <Code text={block.text} />
    case 'action':
      return <Action url={block.url} />
    case 'facts':
      return (
        <Facts>
          {block.facts.map((f) => (
            <FactRow
              key={f.label}
              label={f.label}
              value={f.machine ? <MachineValue>{f.value}</MachineValue> : f.value}
            />
          ))}
        </Facts>
      )
  }
}

export function MessageView({ message }: { message: Message }) {
  return (
    <Layout title={message.subject}>
      {message.blocks.map((b, i) => (
        // The blocks of a message never move: their place is who they are.
        // biome-ignore lint/suspicious/noArrayIndexKey: see above
        <BlockView key={i} block={b} />
      ))}
    </Layout>
  )
}
