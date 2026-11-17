/**
 * The model's notice (ADR 0028, "Consent"): what a person reads and agrees to before Permaily asks a model about
 * their mail. The version is the server's (`ModelNoticeVersion` in internal/domain), which a test there compares
 * with this one: the app and the server ship in one image, so the notice shown is the one the server records. Its
 * words are docs/web.md's ("The model's notice"); a change to them is a new version, and every account is asked
 * again. They say what holds while `zero_data_retention` is false in `deploy/enclave.json`: the release in which
 * OpenAI approves zero data retention for Permaily changes them.
 */
export const MODEL_NOTICE_VERSION = '2026-11-16'

/** The notice's title. */
export const modelNoticeTitle = 'Before a model decides'

/** The notice, one paragraph an entry, in the order they are shown. */
export const modelNotice: readonly string[] = [
  'To decide a condition marked “a model decides”, Permaily sends OpenAI the subject of the message, and for conditions on the whole message its text too. It does so only for the messages its exact conditions did not settle. We do not add sender or recipient header fields, account identifiers or attachment files to the model request. The topic, subject and message text are not anonymised or redacted: they may themselves contain names, email addresses, sign-in information, information about children or other sensitive information. OpenAI receives that content even if the decision is to hold the message back from the app or agent.',
  'OpenAI does not train on what it is sent. Until OpenAI approves zero data retention for Permaily, OpenAI may keep what it is sent for up to 30 days to monitor abuse, and flagged requests and a sample of others for a time it does not state.',
  'Permaily keeps individual cached model answers, sealed, for as long as the message. For the Sign-in and security rule, it also keeps the decision and the model answers supporting it for at least 365 days and one hour from the message’s arrival, even if you delete the message earlier. After that, they are removed when their record is next updated; a record not updated again stays until the mailbox is disconnected or the account is removed and its mailbox data is cleaned up.',
  'Model answers used to explain a connection’s request can also be copied into its receipt. Those copies remain for the lifetime of your account, including after the message is deleted or the mailbox disconnected.',
]
