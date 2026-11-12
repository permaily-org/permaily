/**
 * What to call a browser, from what it says of itself (its User-Agent). The server keeps that text for each
 * session; a person recognises "Firefox on Mac", not the text. Nothing here is exact, and nothing rests on it.
 *
 * The notice by mail of a new device names the browser with the same two tables, in Go: `DeviceName` in
 * internal/domain/accounts.go. Change both together: TestDeviceNameMatchesTheWebApp reads this file and fails
 * when they differ.
 */

const browsers: Array<[RegExp, string]> = [
  [/\bEdg(e|A|iOS)?\//, 'Edge'],
  [/\b(OPR|OPT)\//, 'Opera'],
  [/\bSamsungBrowser\//, 'Samsung Internet'],
  [/\b(Firefox|FxiOS)\//, 'Firefox'],
  [/\b(HeadlessChrome|Chrome|CriOS|Chromium)\//, 'Chrome'],
  [/\bSafari\//, 'Safari'],
]

const systems: Array<[RegExp, string]> = [
  [/\biPhone\b/, 'iPhone'],
  [/\biPad\b/, 'iPad'],
  [/\bAndroid\b/, 'Android'],
  [/\bWindows\b/, 'Windows'],
  [/\bCrOS\b/, 'ChromeOS'],
  [/\b(Macintosh|Mac OS X)\b/, 'Mac'],
  [/\b(Linux|X11)\b/, 'Linux'],
]

function first(table: Array<[RegExp, string]>, text: string): string | null {
  return table.find(([pattern]) => pattern.test(text))?.[1] ?? null
}

/** "Firefox on Mac", or as much of it as can be told. */
export function describeDevice(userAgent: string): string {
  const browser = first(browsers, userAgent)
  const system = first(systems, userAgent)
  if (browser && system) return `${browser} on ${system}`
  return browser ?? system ?? 'Unknown device'
}
