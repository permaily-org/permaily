import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The dev server has no API of its own: it hands /api and /dev to an enclave in dev mode on this machine
// (docs/web.md). PERMAILY_DEV_APP is the app's address on that enclave. The enclave picks the app by host
// name and lets a change through only from the app's own origin, so both are set as the app would send them.
// None of this is part of the build.
const enclave = new URL(process.env.PERMAILY_DEV_APP ?? 'https://app.permaily.com:8443')
const toEnclave = {
  target: `https://127.0.0.1:${enclave.port || '443'}`,
  // The dev enclave signs its own certificate.
  secure: false,
  headers: { host: enclave.host, origin: enclave.origin },
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // Every asset is a file of its own. Vite would otherwise put the smallest font in the stylesheet as a data: URL,
  // which the enclave's Content-Security-Policy (default-src 'self') refuses.
  build: { assetsInlineLimit: 0 },
  server: { proxy: { '/api': toEnclave, '/dev': toEnclave } },
})
