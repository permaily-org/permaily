package decisions

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/domain/mail"
	"github.com/permaily-org/permaily/internal/services/accounts"
)

// Mailer sends the alarm's mail: sesmail.Sender in the image, with its
// watch on copies (ADR 0021). What it returns is never logged: a mail
// system's errors carry addresses.
type Mailer interface {
	Send(ctx context.Context, m accounts.Mail) error
}

// MailAlarm is the Alarm of the image: fixed words for each kind and the
// day, written as domain/mail blocks like the notices, sent to the super
// admin's address compiled into the image. Never an amount tied to an
// account, a topic or anything of a message.
type MailAlarm struct {
	Mailer Mailer
	// To is the super admin's address (deploy/enclave.json, super_admin).
	To string
}

// errAlarmNotSent is Raise's error. It says nothing of the mail system's:
// those carry addresses.
var errAlarmNotSent = errors.New("decisions: the alarm's mail was not sent")

// alarmText is what an alarm of a kind says.
type alarmText struct {
	subject, lead, what string
}

// alarmWords are the alarms' words, one for each kind.
var alarmWords = map[AlarmKind]alarmText{
	AlarmDaySpend: {
		subject: "Permaily: the model's spend passed the day's alarm",
		lead:    "What the enclaves reserved for the model on the day below passed the day's alarm compiled into the image.",
		what:    "Nothing is held back for it: the month's limit still stops every enclave. Look at the bill of the OpenAI project, and at what asks the model, before the month's limit is reached.",
	},
	AlarmMonthLimit: {
		subject: "Permaily: the model's monthly limit was reached",
		lead:    "On the day below, the enclaves reserved the whole of the model's monthly limit compiled into the image. Nothing new is asked of the model until that month ends (UTC).",
		what:    "Answers already kept are still used. Conditions that need a new answer deliver nothing, and rules that protect apply. Only a release changes the limit.",
	},
	AlarmProviderLimit: {
		subject: "Permaily: OpenAI's limit was reached",
		lead:    "OpenAI answered that a spend, credit or usage limit of the project was reached. Nothing new is asked of the model.",
		what:    "The enclave tries one call every ten minutes and asks again once one is answered. Look at the OpenAI project's limits and billing.",
	},
	AlarmKeyRefused: {
		subject: "Permaily: OpenAI refused the model's key",
		lead:    "OpenAI refused the key the enclave holds for the model. Nothing new is asked of the model.",
		what:    "Set a key of the enclave's OpenAI project with scripts/model-key.sh. The enclave tries one call every ten minutes, and a new key at once.",
	},
	AlarmCanaryChanged: {
		subject: "Permaily: the model's canary saw an answer move",
		lead:    "The daily canary saw an answer of the model move from what was measured for the prompt version in force, or another model answer.",
		what:    "The service goes on, and answers already kept do not change. Run the eval on the whole corpus: if the thresholds still hold, a release takes the new answers as the canary's; if not, a release with a new prompt version, or without the model, follows.",
	},
	AlarmCanaryFailed: {
		subject: "Permaily: no canary check completed for two days",
		lead:    "No check of the model's daily canary completed for two days (UTC).",
		what:    "Without a check, a change of the model under its name would not be seen. Look at the enclaves' logs for the canary's failures, and at the guard on /status.",
	},
}

// alarmNote ends every alarm.
const alarmNote = "This message is sent by the enclave. It holds no amount tied to an account and nothing of anyone's mail."

// alarmMail writes the alarm of a kind for a day, and false for a kind
// there are no words for or a day that is not one.
func alarmMail(to string, kind AlarmKind, day string) (accounts.Mail, bool) {
	words, ok := alarmWords[kind]
	if _, err := time.Parse(time.DateOnly, day); !ok || err != nil {
		return accounts.Mail{}, false
	}
	blocks := []mail.Block{
		mail.Lead(words.lead),
		mail.Facts(mail.Fact{Label: "Day (UTC)", Value: day, Machine: true}),
		mail.Paragraph(words.what),
		mail.Note(alarmNote),
	}
	return accounts.Mail{To: to, Subject: words.subject, Blocks: blocks, Text: mail.Text(blocks), HTML: mail.HTML(words.subject, blocks)}, true
}

// Raise sends the alarm of a kind for a day, within AlarmTimeout. It
// returns nil only when the mail system took it.
func (a *MailAlarm) Raise(ctx context.Context, kind AlarmKind, day string) error {
	m, ok := alarmMail(a.To, kind, day)
	if !ok || a.Mailer == nil || a.To == "" {
		return errAlarmNotSent
	}
	ctx, cancel := context.WithTimeout(ctx, AlarmTimeout)
	defer cancel()
	if err := a.Mailer.Send(ctx, m); err != nil {
		return errAlarmNotSent
	}
	return nil
}
