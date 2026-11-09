import { Menu } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Outlet, useLocation } from 'react-router'
import { cx, focusRing } from '../lib/styles.ts'
import { Sidebar } from './Sidebar.tsx'

const wide = '(min-width: 64rem)'
const medium = '(min-width: 48rem)'

/** Collapsed below lg and expanded from lg. The person's own toggle wins until the width crosses lg again. */
function useCollapsed(): [boolean, () => void] {
  const [collapsed, setCollapsed] = useState(() => !window.matchMedia(wide).matches)
  useEffect(() => {
    const query = window.matchMedia(wide)
    const follow = (event: MediaQueryListEvent) => setCollapsed(!event.matches)
    query.addEventListener('change', follow)
    return () => query.removeEventListener('change', follow)
  }, [])
  return [collapsed, () => setCollapsed((v) => !v)]
}

export function AppShell() {
  const [collapsed, toggle] = useCollapsed()
  const { pathname } = useLocation()
  // The drawer belongs to the page it was opened on, so moving to another page closes it.
  const [drawerPath, setDrawerPath] = useState<string | null>(null)
  const drawerOpen = drawerPath === pathname
  const closeDrawer = () => setDrawerPath(null)
  const menuRef = useRef<HTMLButtonElement>(null)
  const wasOpen = useRef(false)

  // The drawer only exists below md, so close it when the window grows past that.
  useEffect(() => {
    const query = window.matchMedia(medium)
    const close = (event: MediaQueryListEvent) => event.matches && setDrawerPath(null)
    query.addEventListener('change', close)
    return () => query.removeEventListener('change', close)
  }, [])

  useEffect(() => {
    if (!drawerOpen) return
    const close = (event: KeyboardEvent) => event.key === 'Escape' && setDrawerPath(null)
    document.addEventListener('keydown', close)
    return () => document.removeEventListener('keydown', close)
  }, [drawerOpen])

  // Focus goes back to the menu button when the drawer closes.
  useEffect(() => {
    if (wasOpen.current && !drawerOpen) menuRef.current?.focus()
    wasOpen.current = drawerOpen
  }, [drawerOpen])

  return (
    <>
      <div inert={drawerOpen} className="flex min-h-svh flex-col md:flex-row">
        <header className="sticky top-0 z-10 flex items-center justify-between border-b border-rule bg-paper py-2 pl-4 pr-2 md:hidden">
          <div className="text-wordmark font-semibold">Permaily</div>
          <button
            ref={menuRef}
            type="button"
            aria-label="Menu"
            aria-expanded={drawerOpen}
            aria-controls="sidebar-drawer"
            onClick={() => setDrawerPath(pathname)}
            className={cx('rounded-sm p-2 text-ink-2 hover:text-ink', focusRing)}
          >
            <Menu aria-hidden size={16} strokeWidth={1.75} />
          </button>
        </header>
        <Sidebar variant="static" collapsed={collapsed} onToggle={toggle} />
        <main className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
      {drawerOpen && (
        <div className="fixed inset-0 z-20 flex md:hidden">
          <div aria-hidden className="absolute inset-0 bg-scrim" onClick={closeDrawer} />
          <Sidebar variant="drawer" onClose={closeDrawer} />
        </div>
      )}
    </>
  )
}
