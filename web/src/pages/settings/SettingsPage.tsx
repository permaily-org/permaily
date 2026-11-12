import { useState } from 'react'
import { SectionHeading, ViewSwitch } from '../../components/ui/index.ts'
import { closingDays } from '../../lib/closing.ts'
import { pagePadding } from '../../lib/styles.ts'
import { applyTheme, readTheme, saveTheme, type Theme } from '../../lib/theme.ts'
import { DeleteAccount } from './account/DeleteAccount.tsx'
import { Profile } from './account/Profile.tsx'
import { SignIn } from './signin/SignIn.tsx'

const themeOptions: Array<{ value: Theme; label: string }> = [
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
  { value: 'system', label: 'Match my system' },
]

export function SettingsPage() {
  return (
    <div className={pagePadding}>
      <h1 className="text-display font-medium">Settings</h1>

      <div className="mt-8 grid grid-cols-1 items-start gap-8 xl:grid-cols-2 xl:gap-14">
        <div className="flex flex-col gap-8">
          <Profile />
          <SignIn />
          <Appearance />
        </div>
        <div className="flex flex-col gap-8">
          <Notifications />
          <YourData />
        </div>
      </div>

      <DeleteAccount />
    </div>
  )
}

function Appearance() {
  const [theme, setTheme] = useState<Theme>(readTheme)

  return (
    <section aria-labelledby="appearance">
      <SectionHeading id="appearance" title="Appearance" />
      <div className="mt-3">
        <ViewSwitch
          label="Appearance"
          options={themeOptions}
          value={theme}
          onChange={(next) => {
            setTheme(next)
            applyTheme(next)
            saveTheme(next)
          }}
        />
      </div>
    </section>
  )
}

/**
 * The mail Permaily sends: only about security and access (ADR 0017), and none of it can be turned off, so
 * there is nothing here to switch.
 */
function Notifications() {
  return (
    <section aria-labelledby="notifications">
      <SectionHeading id="notifications" title="Notifications" />
      <p className="border-b border-rule py-2 text-body text-ink-2">
        Permaily emails you only about security and access: a sign-in from a new device, a change in
        how you sign in or in your contact address, and your account being deleted or blocked. These
        emails can't be turned off.
      </p>
    </section>
  )
}

/** What happens to the account's data today. Nothing here is a control until it works. */
function YourData() {
  return (
    <section aria-labelledby="your-data">
      <SectionHeading id="your-data" title="Your data" />
      <p className="border-b border-rule py-2 text-body text-ink-2">
        Deleting your account below stops it at once and removes it for good after {closingDays}{' '}
        days: your address, your name, your ways to sign in and your devices. Exporting your data
        isn't built yet.
      </p>
    </section>
  )
}
