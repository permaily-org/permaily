import { Navigate, Route, Routes } from 'react-router'
import { AppShell } from './components/AppShell.tsx'
import { GuestOnly, RequireSession } from './components/SessionGate.tsx'
import { AccountsPage } from './pages/accounts/AccountsPage.tsx'
import { ConnectMailboxPage } from './pages/accounts/ConnectMailboxPage.tsx'
import { ForwardingSetupPage } from './pages/accounts/ForwardingSetupPage.tsx'
import { MailboxLayout } from './pages/accounts/MailboxLayout.tsx'
import { ActivityPage } from './pages/activity/ActivityPage.tsx'
import { AccountPage as AdminAccountPage } from './pages/admin/AccountPage.tsx'
import { AccountsPage as AdminAccountsPage } from './pages/admin/AccountsPage.tsx'
import { AdminGate } from './pages/admin/AdminLayout.tsx'
import { AdminsPage } from './pages/admin/AdminsPage.tsx'
import { InvitationsPage } from './pages/admin/InvitationsPage.tsx'
import { LogPage } from './pages/admin/LogPage.tsx'
import { ProvidersPage } from './pages/admin/ProvidersPage.tsx'
import { SignInPage } from './pages/auth/SignInPage.tsx'
import { ConnectAgentPage } from './pages/connections/ConnectAgentPage.tsx'
import { ConnectionDetailPage } from './pages/connections/ConnectionDetailPage.tsx'
import { ConnectionsPage } from './pages/connections/ConnectionsPage.tsx'
import { ConsentAgentPage } from './pages/consent/ConsentAgentPage.tsx'
import { ConsentAppPage } from './pages/consent/ConsentAppPage.tsx'
import { WideningPage } from './pages/consent/WideningPage.tsx'
import { InboxIndexPage } from './pages/inbox/InboxIndexPage.tsx'
import { InboxPage } from './pages/inbox/InboxPage.tsx'
import { LinkPage } from './pages/inbox/Link.tsx'
import { MessagePage } from './pages/inbox/MessagePage.tsx'
import { OverviewPage } from './pages/overview/OverviewPage.tsx'
import { RuleEditorPage } from './pages/rules/RuleEditorPage.tsx'
import { RulesPage } from './pages/rules/RulesPage.tsx'
import { SettingsPage } from './pages/settings/SettingsPage.tsx'

export function App() {
  return (
    <Routes>
      {/* Signing in and signing up: the only screens without a session */}
      <Route element={<GuestOnly />}>
        <Route path="sign-in" element={<SignInPage key="sign-in" mode="sign-in" />} />
        <Route path="sign-up" element={<SignInPage key="sign-up" mode="sign-up" />} />
      </Route>

      <Route element={<RequireSession />}>
        {/*
          Mailboxes and the inbox: in the app's shell, or, for an account that is closing or has not agreed to
          the terms, in a limited one that offers only what the server allows it (ADR 0026).
        */}
        <Route element={<MailboxLayout />}>
          <Route path="inbox" element={<InboxIndexPage />} />
          <Route path="inbox/:mailboxId" element={<InboxPage />} />
          <Route path="inbox/:mailboxId/:messageId" element={<MessagePage />} />
          <Route path="inbox/:mailboxId/:messageId/links/:link" element={<LinkPage />} />
          <Route path="accounts" element={<AccountsPage />} />
        </Route>
        <Route element={<MailboxLayout flow />}>
          <Route path="accounts/:mailboxId/forwarding" element={<ForwardingSetupPage />} />
        </Route>

        <Route element={<AppShell />}>
          <Route index element={<OverviewPage />} />
          <Route path="connections" element={<ConnectionsPage />} />
          <Route path="connections/:vendorId" element={<ConnectionDetailPage />} />
          <Route path="connections/:vendorId/:connectionId" element={<ConnectionDetailPage />} />
          <Route path="rules" element={<RulesPage />} />
          <Route path="rules/:id" element={<RuleEditorPage />} />
          <Route path="activity" element={<ActivityPage />} />
          <Route path="settings" element={<SettingsPage />} />

          {/* The admin panel: anyone who is not an admin is led where any unknown route leads */}
          <Route path="admin" element={<AdminGate />}>
            <Route index element={<Navigate to="accounts" replace />} />
            <Route path="accounts" element={<AdminAccountsPage />} />
            <Route path="accounts/:id" element={<AdminAccountPage />} />
            <Route path="invitations" element={<InvitationsPage />} />
            <Route path="providers" element={<ProvidersPage />} />
            <Route path="admins" element={<AdminsPage />} />
            <Route path="activity" element={<LogPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        </Route>

        {/* Full-page flows without the sidebar */}
        <Route path="connections/new" element={<ConnectAgentPage />} />
        <Route path="accounts/new" element={<ConnectMailboxPage />} />
        <Route path="accounts/new/forwarding" element={<Navigate to="/accounts/new" replace />} />

        {/* Consent screens that apps and agents send people to */}
        <Route path="consent/trip-app" element={<ConsentAppPage />} />
        <Route path="consent/claude" element={<ConsentAgentPage />} />
        <Route path="consent/ledgerly" element={<WideningPage />} />

        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}
