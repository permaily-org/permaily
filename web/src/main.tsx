import '@fontsource-variable/instrument-sans'
import '@fontsource-variable/jetbrains-mono'
import './styles/index.css'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { App } from './App.tsx'
import { takeArrival } from './lib/arrival.ts'
import { SessionProvider } from './lib/session.tsx'
import { applyTheme, readTheme } from './lib/theme.ts'

applyTheme(readTheme())
// A sign-in link or an invitation leaves the address bar before anything else looks at it.
takeArrival()

const root = document.getElementById('root')
if (!root) throw new Error('Missing #root element')

createRoot(root).render(
  <StrictMode>
    <BrowserRouter>
      <SessionProvider>
        <App />
      </SessionProvider>
    </BrowserRouter>
  </StrictMode>,
)
