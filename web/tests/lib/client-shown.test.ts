import { describe, expect, it } from 'vitest'
import { clientShown } from '../../src/lib/api.ts'

// ADR 0040, finding 4: a client is shown so that two documents on one host are told apart, never by the domain
// alone; the same cases as internal/contracts' TestU1AClientIsShownApartFromAnotherOnItsHost.
describe('clientShown', () => {
  it('names a client by its host and document path, as the server does', () => {
    const cases: Record<string, string> = {
      'https://vendor.example/client-a.json': 'vendor.example/client-a.json',
      'https://vendor.example/client-b.json': 'vendor.example/client-b.json',
      'https://vendor.example/v2/client-a.json': 'vendor.example/v2/client-a.json',
      'https://claude.ai/oauth/mcp-oauth-client-metadata':
        'claude.ai/oauth/mcp-oauth-client-metadata',
      'http://localhost:8080/client': 'http://localhost:8080/client',
      'https://': 'https://',
    }
    for (const [id, shown] of Object.entries(cases)) {
      expect(clientShown(id)).toBe(shown)
    }
  })
})
