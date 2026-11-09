import sitemap from '@astrojs/sitemap';
import starlight from '@astrojs/starlight';
import { ExpressiveCodeTheme } from '@astrojs/starlight/expressive-code';
import { defineConfig } from 'astro/config';
import cspInlineHashes from './integrations/csp-inline-hashes.mjs';

// Code blocks stay in ink, with comments one step dimmer. Colour carries meaning on this site, so syntax colours would muddy it.
const codeTheme = (name, type, background, ink, comment) =>
  ExpressiveCodeTheme.fromJSONString(
    JSON.stringify({
      name,
      type,
      colors: { 'editor.background': background, 'editor.foreground': ink },
      tokenColors: [
        { scope: ['comment', 'punctuation.definition.comment'], settings: { foreground: comment } },
      ],
    }),
  );

export default defineConfig({
  site: 'https://permaily.com',
  // Astro hashes every inline script and style and emits a CSP <meta> per page.
  // frame-ancestors and the other header-only directives are set by the host (amplify.yml).
  security: {
    csp: {
      directives: [
        "default-src 'self'",
        "img-src 'self' data:",
        "font-src 'self' data:",
        "connect-src 'self'",
        "base-uri 'none'",
        "object-src 'none'",
        "form-action 'self'",
      ],
      // Pagefind (docs search) compiles WebAssembly.
      scriptDirective: { resources: ["'self'", "'wasm-unsafe-eval'"] },
      // <style> elements stay hash-restricted. Inline style attributes (the demos'
      // custom properties) are allowed through style-src-attr only.
      // TODO(docs/security.md): move them to classes and drop this exception.
      styleDirective: {
        resources: ["'self'", { resource: "'unsafe-inline'", kind: 'attribute' }],
      },
    },
  },
  integrations: [
    sitemap(),
    cspInlineHashes(),
    // The docs are part of this site. Pages live in src/content/docs/docs/, so they build under /docs/.
    starlight({
      title: 'Permaily docs',
      // The website's own 404 page serves the whole site, docs included.
      disable404Route: true,
      customCss: ['./src/styles/docs.css'],
      // Starlight links /favicon.svg itself; these add the fallbacks the website's own pages use.
      head: [
        { tag: 'meta', attrs: { property: 'og:image', content: 'https://permaily.com/og.png' } },
        { tag: 'link', attrs: { rel: 'icon', href: '/favicon.ico', sizes: '32x32' } },
        { tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' } },
      ],
      tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 2 },
      components: {
        Header: './src/components/docs/Header.astro',
        MobileMenuToggle: './src/components/docs/MobileMenuToggle.astro',
        MobileTableOfContents: './src/components/docs/MobileTableOfContents.astro',
        PageFrame: './src/components/docs/PageFrame.astro',
        PageTitle: './src/components/docs/PageTitle.astro',
        Sidebar: './src/components/docs/Sidebar.astro',
        ThemeProvider: './src/components/docs/ThemeProvider.astro',
      },
      expressiveCode: {
        themes: [
          codeTheme('permaily-dark', 'dark', '#0b0c0b', '#f1f1ec', '#85857d'),
          codeTheme('permaily-light', 'light', '#ecece7', '#131312', '#6c6c65'),
        ],
        // Plain blocks with no window frame, whatever the language. A file name stays in the code as a comment, as it does on the website.
        defaultProps: { frame: 'code', overridesByLang: { text: { wrap: true } } },
        frames: { extractFileNameFromCode: false },
        styleOverrides: {
          borderRadius: 'var(--r)',
          borderWidth: '0px',
          codeBackground: 'var(--sunken)',
          codeForeground: 'var(--ink)',
          codeFontFamily: 'var(--font-mono)',
          codeFontSize: '13px',
          codeLineHeight: '1.7',
          codePaddingBlock: 'var(--space-20)',
          codePaddingInline: 'var(--space-20)',
          uiFontFamily: 'var(--font-sans)',
          focusBorder: 'var(--ink)',
          frames: {
            frameBoxShadowCssValue: 'none',
            editorBackground: 'var(--sunken)',
            // Opaque, so code scrolling underneath doesn't show through the copy button. See docs.css for the rest.
            inlineButtonBackground: 'var(--sunken)',
            inlineButtonBackgroundIdleOpacity: '1',
            inlineButtonBackgroundHoverOrFocusOpacity: '1',
            inlineButtonBackgroundActiveOpacity: '1',
            inlineButtonForeground: 'var(--ink-2)',
            inlineButtonBorder: 'var(--ink-2)',
            tooltipSuccessBackground: 'var(--ink)',
            tooltipSuccessForeground: 'var(--paper)',
          },
        },
      },
      // Pages join the sidebar once they have real content. API reference, SDKs, Webhooks and Errors are in the prototype's sidebar and wait for that.
      sidebar: [
        {
          label: 'Start here',
          items: [
            { label: 'Quickstart', slug: 'docs' },
            { label: 'Concepts', slug: 'docs/concepts' },
            { label: 'Consent screen', slug: 'docs/consent-screen' },
          ],
        },
        {
          label: 'Reading mail',
          items: [
            { label: 'Queries', slug: 'docs/queries' },
            { label: 'Fields, types and tokens', slug: 'docs/fields-types-tokens' },
            { label: 'Rules', slug: 'docs/rules' },
            { label: 'Messages', slug: 'docs/messages' },
            { label: 'Receipts', slug: 'docs/receipts' },
          ],
        },
        {
          label: 'Agents',
          items: [{ label: 'Remote MCP', slug: 'docs/remote-mcp' }],
        },
        {
          label: 'Trust',
          items: [
            { label: 'Untrusted content', slug: 'docs/untrusted-content' },
            { label: 'Verifying the build', slug: 'docs/verifying-the-build' },
            { label: 'Trust model', slug: 'docs/trust-model' },
            { label: 'Self-hosting', slug: 'docs/self-hosting' },
          ],
        },
      ],
    }),
  ],
});
