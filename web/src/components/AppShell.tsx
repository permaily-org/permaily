import { Outlet } from 'react-router'
import { Sidebar } from './Sidebar.tsx'

export function AppShell() {
  return (
    <div className="flex min-h-svh">
      <Sidebar />
      <main className="min-w-0 flex-1">
        <Outlet />
      </main>
    </div>
  )
}
