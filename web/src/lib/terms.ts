/**
 * The terms of service and the privacy policy a person agrees to when they make an account, published on the
 * public site. The version is the server's (`TermsVersion` in internal/domain), which a test there compares
 * with this one, and the site's (`TERMS_VERSION` in site/src/config/legal.ts): the app and the server ship in
 * one image, so the sign-up screen always names the version the server takes.
 */
export const TERMS_VERSION = '2026-11-12'

export const termsUrl = 'https://permaily.com/terms/'
export const privacyUrl = 'https://permaily.com/privacy/'
