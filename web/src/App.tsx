import { Navigate, Route, Routes } from 'react-router'
import { AppShell } from './components/AppShell.tsx'
import { ConsentPage } from './pages/ConsentPage.tsx'
import { SectionPage } from './pages/SectionPage.tsx'
import { sections } from './sections.ts'

export function App() {
  return (
    <Routes>
      <Route element={<AppShell />}>
        {sections.map((section) =>
          section.path === '/' ? (
            <Route key={section.path} index element={<SectionPage section={section} />} />
          ) : (
            <Route key={section.path} path={section.path} element={<SectionPage section={section} />} />
          ),
        )}
      </Route>
      <Route path="/consent" element={<ConsentPage />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
