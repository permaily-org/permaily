/**
 * The model's notice (ADR 0028, "Consent"): what a person reads and agrees to before Permaily asks a model about
 * their mail. The version is the server's (`ModelNoticeVersion` in internal/domain), which a test there compares
 * with this one: the app and the server ship in one image, so the notice shown is the one the server records. Its
 * words are docs/web.md's ("The model's notice"); a change to them is a new version, and every account is asked
 * again. They say what holds while `zero_data_retention` is false in `deploy/enclave.json`: the release in which
 * OpenAI approves zero data retention for Permaily changes them.
 */
export const MODEL_NOTICE_VERSION = '2026-11-15'

/** The notice's title. */
export const modelNoticeTitle = 'Before a model decides'

/** The notice, one paragraph an entry, in the order they are shown. */
export const modelNotice: readonly string[] = [
  'To decide a condition marked “a model decides”, Permaily sends OpenAI the subject of the message, and for conditions on the whole message its text too. It does so only for the messages its exact conditions did not settle, and never sends the sender, the recipients or the attachments. Nothing of your account is added.',
  'OpenAI does not train on what it is sent. Until OpenAI approves zero data retention for Permaily, OpenAI may keep what it is sent for up to 30 days to monitor abuse, and flagged requests and a sample of others for a time it does not state.',
  'Permaily keeps what the model answered, sealed, for as long as the message.',
]
