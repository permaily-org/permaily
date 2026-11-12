// Writes one preview file for each fixture, so `bun run dev` (React Email's preview) lists every message the
// service sends, with the sample values the service wrote (fixtures/, from internal/services/accounts). The
// files are not committed: they hold nothing but the name of their fixture.

import { mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const dir = join(import.meta.dir, '..')
const out = join(dir, 'previews')
rmSync(out, { recursive: true, force: true })
mkdirSync(out)
const names = readdirSync(join(dir, 'fixtures'))
  .filter((f) => f.endsWith('.json'))
  .map((f) => f.slice(0, -'.json'.length))
for (const name of names) {
  writeFileSync(
    join(out, `${name}.tsx`),
    `import fixture from '../fixtures/${name}.json'
import { type Message, MessageView } from '../src/message.tsx'

const message = fixture as Message
export default function Preview() {
  return <MessageView message={message} />
}
`,
  )
}
console.log(`mail preview: ${names.length} messages in previews/`)
