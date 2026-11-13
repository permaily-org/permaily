// Writes the mail's templates into the Go tree, where the service embeds them (internal/domain/mail).
//
//   bun scripts/build.ts          # write them
//   bun scripts/build.ts --check  # fail when the committed templates are not what the components make

import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { templates } from '../src/templates.tsx'

export const out = join(import.meta.dir, '../../../internal/domain/mail/templates')

const files = await templates()
const check = process.argv.includes('--check')
const present = existsSync(out) ? readdirSync(out).filter((f) => f.endsWith('.html')) : []
const stale = [
  ...[...files]
    .filter(
      ([name, html]) => !present.includes(name) || readFileSync(join(out, name), 'utf8') !== html,
    )
    .map(([n]) => n),
  ...present.filter((name) => !files.has(name)),
]

if (check) {
  if (stale.length) {
    console.log(
      `mail templates: ${stale.join(', ')} not what the components make; run bun run build in packages/mail`,
    )
    process.exit(1)
  }
  console.log(`mail templates: ${files.size} current`)
} else {
  mkdirSync(out, { recursive: true })
  for (const name of present) if (!files.has(name)) rmSync(join(out, name))
  for (const [name, html] of files) writeFileSync(join(out, name), html)
  console.log(`mail templates: ${files.size} written, ${stale.length} changed`)
}
