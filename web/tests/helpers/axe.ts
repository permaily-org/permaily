import axe from 'axe-core'
import { expect } from 'vitest'

/**
 * Runs axe on what is rendered, with the rules the public site is held to (WCAG 2.0, 2.1 and 2.2, A and AA).
 * jsdom draws nothing, so contrast cannot be measured here: that rule is off, and colours are the design
 * system's (docs/accessibility.md).
 */
export async function expectAccessible(root: Element = document.body): Promise<void> {
  const results = await axe.run(root, {
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'] },
    rules: { 'color-contrast': { enabled: false } },
    // The message's frame is a document of its own that jsdom does not load; its frame element is checked here
    // (its title), its content in real browsers (tests/message-frame).
    iframes: false,
  })
  const found = results.violations.map(
    (violation) =>
      `${violation.id}: ${violation.help} (${violation.nodes.map((node) => node.html).join(' | ')})`,
  )
  expect(found).toEqual([])
}
