// Validates agent skills in skills/<name>/SKILL.md (Agent Skills format):
// YAML frontmatter with a kebab-case `name` equal to the directory name (at
// most 64 characters) and a non-empty `description` (at most 1024 characters).

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

const dir = join(import.meta.dir, '..', 'skills')
const errors: string[] = []
const skills = existsSync(dir)
  ? readdirSync(dir).filter((d) => statSync(join(dir, d)).isDirectory())
  : []

for (const name of skills) {
  const file = join(dir, name, 'SKILL.md')
  if (!existsSync(file)) {
    errors.push(`skills/${name}: missing SKILL.md`)
    continue
  }
  const match = readFileSync(file, 'utf8').match(/^---\n([\s\S]*?)\n---\n/)
  if (!match) {
    errors.push(`skills/${name}/SKILL.md: missing YAML frontmatter`)
    continue
  }
  const fields = Object.fromEntries(
    match[1]
      .split('\n')
      .map((l) => l.match(/^([a-z-]+):\s*(.*)$/))
      .filter((m): m is RegExpMatchArray => m !== null)
      .map((m) => [m[1], m[2].replace(/^["']|["']$/g, '')]),
  )
  if (fields.name !== name)
    errors.push(`skills/${name}: name "${fields.name}" must equal "${name}"`)
  if (!/^[a-z0-9]+(-[a-z0-9]+)*$/.test(name) || name.length > 64) {
    errors.push(`skills/${name}: name must be kebab-case and at most 64 characters`)
  }
  const description = fields.description ?? ''
  if (!description || description.length > 1024) {
    errors.push(`skills/${name}: description must be 1-1024 characters`)
  }
}

for (const e of errors) console.log(e)
console.log(`skills check: ${skills.length} skills, ${errors.length} errors`)
process.exit(errors.length ? 1 : 0)
