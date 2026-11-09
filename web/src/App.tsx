import { Navigate, Route, Routes } from 'react-router'
import { AppShell } from './components/AppShell.tsx'
import { AccountsPage } from './pages/accounts/AccountsPage.tsx'
import { ConnectMailboxPage } from './pages/accounts/ConnectMailboxPage.tsx'
import { ForwardingSetupPage } from './pages/accounts/ForwardingSetupPage.tsx'
import { ActivityPage } from './pages/activity/ActivityPage.tsx'
import { ConnectAgentPage } from './pages/connections/ConnectAgentPage.tsx'
import { ConnectionDetailPage } from './pages/connections/ConnectionDetailPage.tsx'
import { ConnectionsPage } from './pages/connections/ConnectionsPage.tsx'
import { ConsentAgentPage } from './pages/consent/ConsentAgentPage.tsx'
import { ConsentAppPage } from './pages/consent/ConsentAppPage.tsx'
import { WideningPage } from './pages/consent/WideningPage.tsx'
import { OverviewPage } from './pages/overview/OverviewPage.tsx'
import { RuleEditorPage } from './pages/rules/RuleEditorPage.tsx'
import { RulesPage } from './pages/rules/RulesPage.tsx'
import { SettingsPage } from './pages/settings/SettingsPage.tsx'

export function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<OverviewPage />} />
        <Route path="connections" element={<ConnectionsPage />} />
        <Route path="connections/:vendorId" element={<ConnectionDetailPage />} />
        <Route path="connections/:vendorId/:connectionId" element={<ConnectionDetailPage />} />
        <Route path="rules" element={<RulesPage />} />
        <Route path="rules/:id" element={<RuleEditorPage />} />
        <Route path="activity" element={<ActivityPage />} />
        <Route path="accounts" element={<AccountsPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>

      {/* Full-page flows without the sidebar */}
      <Route path="connections/new" element={<ConnectAgentPage />} />
      <Route path="accounts/new" element={<ConnectMailboxPage />} />
      <Route path="accounts/new/forwarding" element={<ForwardingSetupPage />} />

      {/* Consent screens that apps and agents send people to */}
      <Route path="consent/trip-app" element={<ConsentAppPage />} />
      <Route path="consent/claude" element={<ConsentAgentPage />} />
      <Route path="consent/ledgerly" element={<WideningPage />} />

      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
