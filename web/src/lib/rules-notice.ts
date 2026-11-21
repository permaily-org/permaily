/**
 * The rules' notice (ADR 0040, "The rules' notice"): what a person reads and agrees to, once for the account, before
 * Permaily sends their words to a model to make a rule, or saves a rule a model decides any of. It is the rules' own,
 * apart from the model's notice (src/lib/model-notice.ts), so agreeing to it, or a new version of it, stops no
 * connection. The version is the server's (`RulesNoticeVersion` in internal/domain, ADR 0040's pull request 1), which a
 * test compares with this one once it is there: the app and the server ship in one image. Its words are docs/web.md's
 * ("The rules' notice"); a change to them is a new version, and rules a model decides ask nothing until the person
 * agrees again and saves them. They say what holds while `zero_data_retention` is false in `deploy/enclave.json`.
 * First draft, for the designer's and the owner's review.
 */
export const RULES_NOTICE_VERSION = '2026-11-20'

/** The notice's title. */
export const rulesNoticeTitle = 'Before a model helps with your rules'

/** The notice, one paragraph an entry, in the order they are shown. */
export const rulesNotice: readonly string[] = [
  'When you describe a rule in your own words, Permaily sends OpenAI those words and nothing else: not your mail, your mailboxes, your connections or your address. Permaily keeps the words nowhere: you edit the rule by its conditions afterwards.',
  'For a rule with a condition marked “a model decides”, Permaily sends OpenAI what it sends for any such condition: the subject of the message, and for conditions on the whole message its text too, only for the messages its exact conditions did not settle.',
  'For a rule that takes out sentences about a topic, Permaily sends OpenAI the subject and the text of each message the rule applies to, sentence by sentence, with every email address, phone, card, account and other number, and link hidden. It never sends the sender, the recipients or attachments. The text is not otherwise anonymised: it may contain names or other sensitive information.',
  'OpenAI does not train on what it is sent. Until OpenAI approves zero data retention for Permaily, OpenAI may keep what it is sent for up to 30 days to monitor abuse, and flagged requests and a sample of others for a time it does not state.',
  'Permaily keeps each rule’s decisions about a message, sealed, as it keeps the Sign-in and security rule’s, so that what a rule held back or took out stays so: for at least 365 days and one hour from the message’s arrival, then until their record is next updated, the mailbox is disconnected or the account is removed. A connection’s receipt says what a rule took out, never what the text was.',
]
