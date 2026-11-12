import { TextAnchor } from '../../components/ui/index.ts'
import { supportAddress } from './messages.ts'

/**
 * What a sign-in says when it proved who it is and ended at an account the operator blocked: that nobody was
 * signed in, and where to write. The words are fixed and name nobody: the reason can come from the address
 * bar, which anyone can write. Trying again would only fail the same way, so nothing here offers to.
 */
export function BlockedWords() {
  return (
    <>
      Nobody was signed in. To ask about it, write to{' '}
      <TextAnchor href={`mailto:${supportAddress}`}>{supportAddress}</TextAnchor>.
    </>
  )
}
