package contracts

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The web app reads these names (web/src/lib/api.ts), vendors read the
// vendor's and the notice's: a change here is a change of ADR 0039.
func TestWebhookShapes(t *testing.T) {
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	weekly := &WebhookSchedule{Every: EveryWeek, On: "monday", At: "08:00"}
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"what a vendor asks", WebhookRequest{URL: "https://trip-app.com/hooks/permaily", NewMail: true, Schedule: weekly},
			`{"url":"https://trip-app.com/hooks/permaily","new_mail":true,"schedule":{"every":"week","on":"monday","at":"08:00"}}`},
		{"every day", WebhookSchedule{Every: EveryDay, At: "17:45"}, `{"every":"day","at":"17:45"}`},
		{"what the screen shows", WebhookAsked{Host: "trip-app.com", NewMail: true}, `{"host":"trip-app.com","new_mail":true}`},
		{"what the person sees, failing", ConnectionWebhook{Host: "trip-app.com", Schedule: weekly, TimeZone: "Europe/Madrid", State: WebhookOn, NextAt: at, LastSentAt: at, FailingSince: at, Failure: WebhookNoAnswer},
			`{"host":"trip-app.com","schedule":{"every":"week","on":"monday","at":"08:00"},"time_zone":"Europe/Madrid","state":"on","next_at":"2026-11-20T08:00:00Z","last_sent_at":"2026-11-20T08:00:00Z","failing_since":"2026-11-20T08:00:00Z","failure":"no_answer"}`},
		{"turned off", ConnectionWebhook{Host: "trip-app.com", NewMail: true, State: WebhookOff}, `{"host":"trip-app.com","new_mail":true,"state":"off"}`},
		{"what the vendor is told", VendorWebhook{Subscription: "sub_x", URL: "https://trip-app.com/hooks/permaily", NewMail: true, State: WebhookStopped},
			`{"subscription":"sub_x","url":"https://trip-app.com/hooks/permaily","new_mail":true,"state":"stopped"}`},
		{"a notice of mail", WebhookNotice{V: WebhookNoticeVersion, Type: NoticeMailMatched, ID: "ntc_x", Subscription: "sub_x", ClientID: "https://trip-app.com/client.json", Audience: "https://trip-app.com/hooks/permaily", CreatedAt: at, Since: at},
			`{"v":1,"type":"mail.matched","id":"ntc_x","subscription":"sub_x","client_id":"https://trip-app.com/client.json","aud":"https://trip-app.com/hooks/permaily","created_at":"2026-11-20T08:00:00Z","since":"2026-11-20T08:00:00Z"}`},
		{"a late notice of a schedule", WebhookNotice{V: WebhookNoticeVersion, Type: NoticeScheduleDue, ID: "ntc_y", Subscription: "sub_x", ClientID: "https://trip-app.com/client.json", Audience: "https://trip-app.com/hooks/permaily", CreatedAt: at, Since: at, ScheduledAt: at, Late: true},
			`{"v":1,"type":"schedule.due","id":"ntc_y","subscription":"sub_x","client_id":"https://trip-app.com/client.json","aud":"https://trip-app.com/hooks/permaily","created_at":"2026-11-20T08:00:00Z","since":"2026-11-20T08:00:00Z","scheduled_at":"2026-11-20T08:00:00Z","late":true}`},
		{"a request's webhook", AuthorizationRequest{Webhook: &WebhookAsked{Host: "trip-app.com", Schedule: weekly}}, ""},
		{"an approval in a time zone", AuthorizationDecision{Allow: true, TimeZone: "America/New_York"}, `{"allow":true,"time_zone":"America/New_York"}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.want == "" {
			if !strings.Contains(string(b), `"webhook":{"host":"trip-app.com","schedule":{"every":"week","on":"monday","at":"08:00"}}`) {
				t.Errorf("%s: %s", c.name, b)
			}
			continue
		}
		if string(b) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, b, c.want)
		}
	}
}

// A connection, a request and what a vendor is told without a webhook keep
// the shapes ADR 0030 pinned (TestConnectionShapes): the field is absent.
func TestNoWebhookLeavesTheShapesOfADR0030(t *testing.T) {
	for _, v := range []any{AuthorizationRequest{}, AuthorizationDecision{}, Connection{}, VendorConnection{}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "webhook") || strings.Contains(string(b), "time_zone") {
			t.Errorf("%T: %s", v, b)
		}
	}
}

// A notice holds these members and no other (ADR 0039, "The notice"): a
// member added to it is a change of what a vendor's and a host's logs may
// see, so it is a change of this list and of the ADR.
func TestANoticeHoldsOnlyWhatTheADRSays(t *testing.T) {
	want := []string{"aud", "client_id", "created_at", "id", "late", "scheduled_at", "since", "subscription", "type", "v"}
	var got []string
	rt := reflect.TypeFor[WebhookNotice]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("a notice's members: %v, want %v", got, want)
	}
}
