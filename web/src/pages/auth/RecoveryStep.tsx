import { type FormEvent, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Button, Headline, TextAction } from '../../components/ui/index.ts'
import { errorCode, recover } from '../../lib/api.ts'
import { caretAfter } from '../../lib/codes.ts'
import { recoveryCode } from '../../lib/recoveryCode.ts'
import { useSession } from '../../lib/session.tsx'
import { AuthLayout } from './AuthLayout.tsx'
import { Field } from './Field.tsx'
import { messageFor, recoveryRefused } from './messages.ts'

/**
 * The way in for someone who lost every passkey of an account that signs in with passkeys only: the account's
 * address and one recovery code. The code is typed with or without dashes, in any case, and lives only in this
 * step's memory. Every refusal reads the same, so the screen never says whether an address has an account.
 */
export function RecoveryStep({
  address,
  onAddress,
  onBack,
}: {
  address: string
  onAddress: (address: string) => void
  onBack: () => void
}) {
  const session = useSession()
  const [code, setCode] = useState('')
  const [errors, setErrors] = useState<{ address?: string; code?: string; form?: string }>({})
  const [pending, setPending] = useState(false)
  const addressRef = useRef<HTMLInputElement>(null)
  const codeRef = useRef<HTMLInputElement>(null)
  // Where the caret goes once the code is written in groups again.
  const caret = useRef<number | null>(null)

  // The first thing left to fill in takes focus.
  // biome-ignore lint/correctness/useExhaustiveDependencies: only when the step opens
  useEffect(() => {
    if (address.trim()) codeRef.current?.focus()
    else addressRef.current?.focus()
  }, [])

  // biome-ignore lint/correctness/useExhaustiveDependencies: runs after each change of the code
  useLayoutEffect(() => {
    if (caret.current === null) return
    codeRef.current?.setSelectionRange(caret.current, caret.current)
    caret.current = null
  }, [code])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    const typed = address.trim()
    if (!typed.includes('@')) {
      setErrors({ address: 'Enter your email address.' })
      addressRef.current?.focus()
      return
    }
    const normalized = recoveryCode.normalize(code)
    if (!normalized) {
      setErrors({
        code: 'A recovery code has 16 letters and digits. Check it against the ones you kept.',
      })
      codeRef.current?.focus()
      return
    }
    setErrors({})
    setPending(true)
    try {
      session.signedIn(await recover(typed, normalized))
    } catch (failure) {
      setPending(false)
      const failed = errorCode(failure)
      // The server refuses a wrong code, a used one, an address without an account and too many tries alike.
      const refused = failed !== 'network' && failed !== 'other' && failed !== 'unauthorized'
      setErrors({ form: refused ? recoveryRefused : messageFor(failed) })
      codeRef.current?.focus()
    }
  }

  return (
    <AuthLayout title="Use a recovery code">
      <Headline size="display-s">Use a recovery code</Headline>
      <p id="recovery-intro" className="mt-3 text-body text-ink-2">
        For an account that signs in with passkeys only, when you can't use any of them. Each code
        works once. Once you're in, add a new passkey in Settings.
      </p>

      <form noValidate onSubmit={submit} className="mt-8 flex flex-col gap-5">
        <Field
          id="recovery-address"
          label="Email address"
          type="email"
          name="email"
          autoComplete="email"
          inputMode="email"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          required
          inputRef={addressRef}
          value={address}
          onChange={(event) => onAddress(event.target.value)}
          error={errors.address}
        />
        <Field
          id="recovery-code"
          label="Recovery code"
          type="text"
          name="recovery-code"
          autoComplete="off"
          autoCapitalize="characters"
          autoCorrect="off"
          spellCheck={false}
          required
          placeholder={recoveryCode.shape}
          className="font-mono"
          inputRef={codeRef}
          value={code}
          onChange={(event) => {
            const typed = event.target.value
            const formatted = recoveryCode.format(typed)
            const position = event.target.selectionStart ?? typed.length
            caret.current = caretAfter(formatted, recoveryCode.charactersBefore(typed, position))
            setCode(formatted)
          }}
          error={errors.code}
          describedBy="recovery-intro"
        />
        <p role="alert" className="text-small text-danger empty:hidden">
          {errors.form}
        </p>
        <Button type="submit" disabled={pending}>
          Sign in
        </Button>
      </form>

      <div className="mt-8 flex flex-wrap gap-x-5 gap-y-2 border-t border-rule pt-4">
        <TextAction onClick={onBack} disabled={pending}>
          Sign in another way
        </TextAction>
      </div>
    </AuthLayout>
  )
}
