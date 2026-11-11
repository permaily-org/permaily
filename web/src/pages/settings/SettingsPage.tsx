import { useState } from 'react'
import { useNavigate } from 'react-router'
import {
  Button,
  Dialog,
  InfoTip,
  SectionHeading,
  TextAction,
  ViewSwitch,
} from '../../components/ui/index.ts'
import { connections, user } from '../../data/sample.ts'
import { pagePadding } from '../../lib/styles.ts'
import { applyTheme, readTheme, saveTheme, type Theme } from '../../lib/theme.ts'
import { solidDanger } from '../accounts/styles.ts'
import { ExportReady } from './ExportReady.tsx'
import { EditableRow, NotificationRow, SettingRow } from './rows.tsx'
import { SignIn } from './signin/SignIn.tsx'

const notificationOptions = [
  { id: 'requests', title: 'New connection requests', on: true },
  { id: 'widening', title: 'Requests for more access', on: true },
  { id: 'unusual', title: 'A rule matching far more than usual', on: true },
  { id: 'unused', title: 'Connections unused for 60 days', on: true },
  { id: 'weekly', title: 'Weekly summary of what left', on: false },
]

const themeOptions: Array<{ value: Theme; label: string }> = [
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
  { value: 'system', label: 'Match my system' },
]

export function SettingsPage() {
  const [deleting, setDeleting] = useState(false)

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

      <section
        aria-labelledby="delete-account"
        className="mt-8 flex flex-col items-start gap-3 border border-danger px-4 py-4 sm:flex-row sm:items-center sm:gap-6 sm:px-5"
      >
        <h2 id="delete-account" className="min-w-0 flex-1 font-medium">
          Delete your account
        </h2>
        <Button variant="danger" onClick={() => setDeleting(true)}>
          Delete my account
        </Button>
      </section>

      <DeleteAccountDialog open={deleting} onClose={() => setDeleting(false)} />
    </div>
  )
}

function Profile() {
  const [name, setName] = useState(user.name)
  const [email, setEmail] = useState(user.email)
  const [language, setLanguage] = useState('English')

  return (
    <section aria-labelledby="profile">
      <SectionHeading id="profile" title="Profile" />
      <EditableRow label="Name" value={name} onSave={setName} actionLabel="Edit" />
      <EditableRow label="Contact email" value={email} onSave={setEmail} type="email" mono />
      <EditableRow
        label="Language"
        value={language}
        onSave={setLanguage}
        options={['English', 'Español', 'Català', 'Français', 'Deutsch']}
      />
    </section>
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

function Notifications() {
  const [on, setOn] = useState(
    () => new Set(notificationOptions.filter((n) => n.on).map((n) => n.id)),
  )

  function toggle(id: string, checked: boolean) {
    setOn((current) => {
      const next = new Set(current)
      if (checked) next.add(id)
      else next.delete(id)
      return next
    })
  }

  return (
    <section aria-labelledby="notifications">
      <SectionHeading id="notifications" title="Notifications" />
      {notificationOptions.map((option) => (
        <NotificationRow
          key={option.id}
          title={option.title}
          checked={on.has(option.id)}
          onChange={(checked) => toggle(option.id, checked)}
        />
      ))}
    </section>
  )
}

function YourData() {
  const [retention, setRetention] = useState('Kept for 1 year')
  const [exported, setExported] = useState(false)

  return (
    <section aria-labelledby="your-data">
      <SectionHeading id="your-data" title="Your data" />
      <EditableRow
        label="Receipts"
        value={retention}
        onSave={setRetention}
        options={['Kept for 30 days', 'Kept for 90 days', 'Kept for 1 year', 'Kept for 2 years']}
      />
      <SettingRow label="Queued mail">
        <span className="inline-flex items-center gap-1.5">
          0 emails
          <InfoTip label="When queued mail is deleted" align="start">
            Deleted after delivery, or after 7 days
          </InfoTip>
        </span>
      </SettingRow>
      <SettingRow
        label="Export"
        action={
          exported ? undefined : <TextAction onClick={() => setExported(true)}>Export</TextAction>
        }
      >
        Rules, connections and receipts. No email
      </SettingRow>
      {exported && <ExportReady />}
    </section>
  )
}

function DeleteAccountDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate()

  return (
    <Dialog open={open} onClose={onClose} label="Delete your account">
      <h2 className="text-heading font-medium">Delete your account?</h2>
      <p className="mt-3 text-body text-ink-2">
        All {connections.length} connections lose access now, and your rules and receipts are
        deleted. This can't be undone, and Permaily can't recall mail connections have already
        received.
      </p>
      <div className="mt-6 flex flex-wrap gap-3">
        <button
          type="button"
          className={solidDanger}
          onClick={() => {
            onClose()
            navigate('/')
          }}
        >
          Delete everything
        </button>
        <Button variant="secondary" onClick={onClose}>
          Keep my account
        </Button>
      </div>
    </Dialog>
  )
}
