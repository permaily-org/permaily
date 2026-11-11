import { type ReactNode, useEffect } from 'react'

/** The page around signing in: the wordmark, then one narrow column. */
export function AuthLayout({ title, children }: { title: string; children: ReactNode }) {
  // The tab names the screen while it shows, and goes back to the app's name after.
  useEffect(() => {
    const before = document.title
    document.title = `${title} · Permaily`
    return () => {
      document.title = before
    }
  }, [title])

  return (
    <div className="mx-auto flex min-h-svh w-full max-w-7xl flex-col px-4 pt-6 pb-10 sm:px-8 sm:pt-8 sm:pb-16 lg:px-12">
      <header>
        <span className="text-wordmark font-semibold">Permaily</span>
      </header>
      <main className="mx-auto mt-10 w-full max-w-sm sm:mt-24">{children}</main>
    </div>
  )
}
