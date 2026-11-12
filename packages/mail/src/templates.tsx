// Writes the Go html/template files of the mail from the components: the layout, and one template for each
// kind of block. Values are markers (@@.Name@@) and the template's control flow is a few elements of our own
// (<pm-range>, <pm-if>, <pm-else>, <pm-blocks>); both become html/template actions once React has rendered.
// The output is the same, byte for byte, on any machine: no time, no random identifier, nothing of the build.

import { pretty, render } from '@react-email/render'
import { createElement, type ReactNode } from 'react'
import {
  Action,
  Code,
  FactRow,
  Facts,
  kinds,
  Layout,
  Lead,
  MachineValue,
  Note,
  Paragraph,
} from './blocks.tsx'

const own = (name: string, attrs: Record<string, string> | null, ...children: ReactNode[]) =>
  createElement(name, attrs, ...children)

const blocks: Record<(typeof kinds)[number], ReactNode> = {
  lead: <Lead text="@@.Text@@" />,
  paragraph: <Paragraph text="@@.Text@@" />,
  note: <Note text="@@.Text@@" />,
  code: <Code text="@@.Text@@" />,
  action: <Action url="@@.URL@@" />,
  facts: (
    <Facts>
      {own(
        'pm-range',
        { of: '.Facts' },
        <FactRow
          label="@@.Label@@"
          value={
            <>
              {own('pm-if', { test: '.Machine' }, <MachineValue>@@.Value@@</MachineValue>)}
              {own('pm-else', null, '@@.Value@@')}
            </>
          }
        />,
      )}
    </Facts>
  ),
}

const header = (what: string) =>
  `{{/* ${what}. Written by packages/mail (bun run build) from its components: do not edit. */}}\n`

/** Turns the markers and our own elements into html/template actions, and fails on any left over. */
export function actions(html: string): string {
  const out = html
    .replace(/@@(\.[A-Z][A-Za-z]*)@@/g, '{{$1}}')
    .replace(/<pm-range of="(\.[A-Z][A-Za-z]*)">/g, '{{range $1}}')
    .replace(/<\/pm-range>/g, '{{end}}')
    .replace(/<pm-if test="(\.[A-Z][A-Za-z]*)">/g, '{{if $1}}')
    .replace(/<\/pm-if>/g, '')
    .replace(/<pm-else>/g, '{{else}}')
    .replace(/<\/pm-else>/g, '{{end}}')
    .replace(/<pm-blocks><\/pm-blocks>/g, '{{range .Blocks}}{{template "block" .}}{{end}}')
  if (/@@|<\/?pm-/.test(out)) throw new Error(`a marker was left in a template: ${out}`)
  return out
}

/**
 * Renders a component as readable HTML. Comments go: React's own markers, and Outlook's conditional ones, which
 * html/template would drop in any case, so the file says what the service sends.
 */
async function html(node: ReactNode): Promise<string> {
  const raw = (await render(node)).replace(/^<!DOCTYPE[^>]*>/, '').replace(/<!--[\s\S]*?-->/g, '')
  return (await pretty(actions(raw), { printWidth: 100 })).trim()
}

/** Every template file, by name, with its content. */
export async function templates(): Promise<Map<string, string>> {
  const files = new Map<string, string>()
  const page = await html(<Layout title="@@.Subject@@">{own('pm-blocks', null)}</Layout>)
  const dispatch = kinds
    .map((k, i) => `${i ? '{{else if' : '{{if'} eq .Kind "${k}"}}{{template "${k}" .}}`)
    .join('')
  files.set(
    'layout.html',
    `${header('The page around a message')}{{define "message"}}<!DOCTYPE html>\n${page}\n{{end}}\n` +
      `{{define "block"}}${dispatch}{{end}}{{end}}\n`,
  )
  for (const kind of kinds) {
    const block = await html(blocks[kind])
    files.set(
      `${kind}.html`,
      `${header(`A block of kind ${kind}`)}{{define "${kind}"}}\n${block}\n{{end}}\n`,
    )
  }
  return files
}
