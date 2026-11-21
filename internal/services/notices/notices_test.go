package notices

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

var at = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// valid is a notice of each kind with exactly the facts its kind has.
func valid() []Notice {
	end := at.AddDate(0, 0, 90)
	conn := Facts{Vendor: "claude.ai", Resource: contracts.ResourceMCP, Parts: []contracts.Part{contracts.PartSender}, EndsAt: end}
	replaced := conn
	replaced.Replaced = true
	added := Facts{Vendor: "claude.ai", Parts: []contracts.Part{contracts.PartSender, contracts.PartBody}, EndsAt: end}
	ends := Facts{Vendor: "claude.ai", EndsAt: end}
	n := func(k Kind, f Facts) Notice {
		return Notice{Account: "acc_ana2", Kind: k, Key: Key("acc_ana2", k, "x"), At: at, Facts: f}
	}
	return []Notice{
		n(KindConnectionApproved, conn), n(KindConnectionApproved, replaced), n(KindDeviceAdded, added),
		n(KindConnectionRenewed, ends), n(KindConnectionEnding, ends), n(KindDeviceEndedReuse, Facts{Vendor: "claude.ai"}),
		n(KindReceiptsIncident, Facts{}), n(KindDevicesSignedOut, Facts{By: ByOperator}), n(KindDevicesSignedOut, Facts{By: ByAdmin}),
	}
}

// E24's rule in the seam: Check takes a notice of every kind with its
// facts, and refuses one whose account, kind, key, time or facts are not
// its kind's.
func TestE24CheckTakesEveryKindWithItsFactsAndNothingElse(t *testing.T) {
	seen := map[Kind]bool{}
	for _, n := range valid() {
		if err := n.Check(); err != nil {
			t.Fatalf("%s %+v refused: %v", n.Kind, n.Facts, err)
		}
		seen[n.Kind] = true
	}
	if len(seen) != len(Kinds()) {
		t.Fatalf("checked %d kinds of %d", len(seen), len(Kinds()))
	}
	bad := []func(*Notice){
		func(n *Notice) { n.Account = "" },
		func(n *Notice) { n.Account = "acc_" },
		func(n *Notice) { n.Account = "con_ana" },
		func(n *Notice) { n.Account = "acc_Ana" },
		func(n *Notice) { n.Account = "acc_" + string(make([]byte, 65)) },
		func(n *Notice) { n.Kind = "sign_in" },
		func(n *Notice) { n.Key = "" },
		func(n *Notice) { n.Key = "ABCDEF0123456789abcdef0123456789" },
		func(n *Notice) { n.Key += "0" },
		func(n *Notice) { n.At = time.Time{} },
	}
	for i, change := range bad {
		n := valid()[0]
		change(&n)
		if err := n.Check(); !errors.Is(err, ErrInvalid) {
			t.Errorf("change %d: Check = %v, want ErrInvalid", i, err)
		}
	}
	facts := []struct {
		kind  Kind
		facts Facts
	}{
		{KindConnectionApproved, Facts{EndsAt: at}},                                 // no vendor
		{KindConnectionApproved, Facts{Vendor: "claude.ai"}},                        // no end
		{KindDeviceAdded, Facts{Vendor: "claude.ai", EndsAt: at, Replaced: true}},   // only an approval replaces
		{KindDeviceEndedReuse, Facts{Vendor: "claude.ai", EndsAt: at}},              // a device's end says no connection's end
		{KindReceiptsIncident, Facts{Vendor: "claude.ai"}},                          // names no vendor
		{KindReceiptsIncident, Facts{Resource: contracts.ResourceAPI}},              // nor a resource
		{KindReceiptsIncident, Facts{Parts: []contracts.Part{contracts.PartBody}}},  // nor parts
		{KindReceiptsIncident, Facts{EndsAt: at}},                                   // nor an end
		{KindDevicesSignedOut, Facts{}},                                             // says who
		{KindDevicesSignedOut, Facts{By: "person"}},                                 // operator or admin only
		{KindConnectionEnding, Facts{Vendor: "claude.ai", EndsAt: at, By: ByAdmin}}, // only a sign-out says who
	}
	for _, c := range facts {
		n := Notice{Account: "acc_ana", Kind: c.kind, Key: Key("acc_ana", c.kind), At: at, Facts: c.facts}
		if err := n.Check(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s with %+v: Check = %v, want ErrInvalid", c.kind, c.facts, err)
		}
	}
}

// E20's rule in the seam: a key is fixed by the account, the kind and the
// event, tells them apart, and carries nothing readable of them.
func TestE20KeysNameTheEventAndNothingReadable(t *testing.T) {
	a := Key("acc_ana", KindConnectionApproved, "rcp_1")
	if a != Key("acc_ana", KindConnectionApproved, "rcp_1") || !keyForm(a) {
		t.Fatalf("key %q is not fixed or not 32 hexadecimal digits", a)
	}
	others := []string{
		Key("acc_bob", KindConnectionApproved, "rcp_1"),
		Key("acc_ana", KindDeviceAdded, "rcp_1"),
		Key("acc_ana", KindConnectionApproved, "rcp_2"),
		Key("acc_ana", KindConnectionApproved, "rcp_", "1"), // parts are kept apart
		EndingKey("acc_ana", "con_1", at),
		EndingKey("acc_ana", "con_1", at.Add(time.Nanosecond)),
		IncidentKey("acc_ana", at),
		IncidentKey("acc_ana", at.AddDate(0, 0, 1)),
	}
	seen := map[string]bool{a: true}
	for _, k := range others {
		if seen[k] {
			t.Fatalf("two events share the key %q", k)
		}
		seen[k] = true
	}
	// One day is one incident key, whatever the hour or the zone.
	if IncidentKey("acc_ana", at) != IncidentKey("acc_ana", at.Add(13*time.Hour).In(time.FixedZone("x", -11*3600))) {
		t.Fatal("two moments of one UTC day have two incident keys")
	}
	if EndingKey("acc_ana", "con_1", at) != EndingKey("acc_ana", "con_1", at.In(time.FixedZone("y", 3600))) {
		t.Fatal("one end in two zones has two keys")
	}
}

// E32's rule: a connection is told of its end once its end is
// EndingBefore away or less, before it ends, only when it was approved or
// renewed for more than EndingMinDays.
func TestE32EndingDue(t *testing.T) {
	end := at.AddDate(0, 0, 90)
	cases := []struct {
		days int
		now  time.Time
		due  bool
	}{
		{90, end.Add(-EndingBefore - time.Nanosecond), false},
		{90, end.Add(-EndingBefore), true},
		{90, end.Add(-time.Nanosecond), true},
		{90, end, false},
		{90, end.Add(time.Hour), false},
		{EndingMinDays + 1, end.Add(-time.Hour), true},
		{EndingMinDays, end.Add(-time.Hour), false},
		{1, end.Add(-time.Hour), false},
	}
	for _, c := range cases {
		if got := EndingDue(c.days, end, c.now); got != c.due {
			t.Errorf("EndingDue(%d, end, end%+v) = %v, want %v", c.days, c.now.Sub(end), got, c.due)
		}
	}
}

// ADR 0041, "Changed after the design review", 6: every kind has exactly the facts its words say, each from a fixed
// set, and nothing else. These are the review's counterexamples: each passed Check before.
func TestE24CheckHoldsEachKindToItsFacts(t *testing.T) {
	long := strings.Repeat("a", 64) + "."
	cases := []struct {
		name   string
		change func(*Notice)
	}{
		{"an approval without its resource", func(n *Notice) { n.Facts.Resource = "" }},
		{"an approval with a resource not of the list", func(n *Notice) { n.Facts.Resource = "anything" }},
		{"an approval without its parts", func(n *Notice) { n.Facts.Parts = nil }},
		{"an approval with a part not of the list", func(n *Notice) { n.Facts.Parts = []contracts.Part{"sender", "everything"} }},
		{"an approval with a part twice", func(n *Notice) { n.Facts.Parts = []contracts.Part{"sender", "sender"} }},
		{"an approval without the sender, which every connection reads", func(n *Notice) { n.Facts.Parts = []contracts.Part{"subject"} }},
		{"an approval saying who", func(n *Notice) { n.Facts.By = "anything" }},
		{"an approval naming an operator", func(n *Notice) { n.Facts.By = ByOperator }},
		{"a vendor of several megabytes", func(n *Notice) { n.Facts.Vendor = strings.Repeat("a", 4<<20) + ".ai" }},
		{"a vendor past a domain's length", func(n *Notice) { n.Facts.Vendor = strings.Repeat(long, 4) + "ai" }},
		{"a vendor's label past 63", func(n *Notice) { n.Facts.Vendor = long + "ai" }},
		{"a vendor in capitals", func(n *Notice) { n.Facts.Vendor = "Claude.ai" }},
		{"a vendor with a space", func(n *Notice) { n.Facts.Vendor = "claude ai" }},
		{"a vendor as a URL", func(n *Notice) { n.Facts.Vendor = "https://claude.ai" }},
		{"a vendor with an empty label", func(n *Notice) { n.Facts.Vendor = "claude..ai" }},
		{"a vendor ending in a dot", func(n *Notice) { n.Facts.Vendor = "claude.ai." }},
		{"a vendor's label starting with a hyphen", func(n *Notice) { n.Facts.Vendor = "-claude.ai" }},
		{"a vendor's label ending with a hyphen", func(n *Notice) { n.Facts.Vendor = "claude-.ai" }},
		{"a vendor of one label", func(n *Notice) { n.Facts.Vendor = "localhost" }},
		{"an end before the event", func(n *Notice) { n.Facts.EndsAt = n.At.Add(-time.Second) }},
		{"an end at the event", func(n *Notice) { n.Facts.EndsAt = n.At }},
		{"a time past its bound", func(n *Notice) { n.At = timeMax }},
		{"a time before its bound", func(n *Notice) { n.At = timeMin.Add(-time.Nanosecond) }},
		{"an end past its bound", func(n *Notice) { n.Facts.EndsAt = timeMax }},
	}
	for _, c := range cases {
		n := valid()[0]
		c.change(&n)
		if err := n.Check(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Check = %v, want ErrInvalid", c.name, err)
		}
	}
	at := at
	conn := Facts{Vendor: "claude.ai", Resource: contracts.ResourceMCP, Parts: []contracts.Part{contracts.PartSender}, EndsAt: at.AddDate(0, 0, 90)}
	without := func(f Facts, drop func(*Facts)) Facts { drop(&f); return f }
	forbidden := []struct {
		kind  Kind
		facts Facts
	}{
		{KindDeviceAdded, conn}, // says no resource
		{KindDeviceAdded, without(conn, func(f *Facts) { f.Resource, f.Parts = "", nil })}, // reads its parts
		{KindConnectionRenewed, without(conn, func(f *Facts) { f.Resource = "" })},         // says no parts
		{KindConnectionRenewed, without(conn, func(f *Facts) { f.Parts = nil })},           // says no resource
		{KindConnectionEnding, without(conn, func(f *Facts) { f.Parts = nil })},            // says no resource
		{KindConnectionEnding, without(conn, func(f *Facts) { f.Resource = "" })},          // says no parts
		{KindDeviceEndedReuse, Facts{Vendor: "claude.ai", Resource: contracts.ResourceAPI}},
		{KindDeviceEndedReuse, Facts{Vendor: "claude.ai", Parts: []contracts.Part{contracts.PartSender}}},
		{KindDeviceEndedReuse, Facts{Vendor: "claude.ai", By: ByAdmin}},
		{KindReceiptsIncident, Facts{By: ByOperator}},
		{KindDevicesSignedOut, Facts{By: ByOperator, Vendor: "claude.ai"}},
		{KindDevicesSignedOut, Facts{By: ByOperator, Replaced: true}},
		{KindDevicesSignedOut, Facts{By: ByOperator, EndsAt: at.Add(time.Hour)}},
	}
	for _, c := range forbidden {
		n := Notice{Account: "acc_ana", Kind: c.kind, Key: Key("acc_ana", c.kind), At: at, Facts: c.facts}
		if err := n.Check(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s with %+v: Check = %v, want ErrInvalid", c.kind, c.facts, err)
		}
	}
	for _, d := range []string{"claude.ai", "a.b", "x-1.example.co.uk", strings.Repeat("a", 63) + ".ai"} {
		n := valid()[0]
		n.Facts.Vendor = d
		if err := n.Check(); err != nil {
			t.Errorf("vendor %q refused: %v", d, err)
		}
	}
}

// ADR 0041, "Changed after the design review", 6: the largest notice Check takes is under NoticeMax once serialized,
// so a notice is small before it is stored, and the queue refuses an entry past EntryMax before it writes it.
func TestE24TheLargestNoticeIsUnderItsBound(t *testing.T) {
	label := strings.Repeat("z", 63)
	n := Notice{
		Account: "acc_" + strings.Repeat("9", 64), Kind: KindConnectionApproved,
		Key: Key("acc_x", KindConnectionApproved), At: timeMax.Add(-time.Second).In(time.FixedZone("x", 14*3600+59*60)),
		Facts: Facts{
			Vendor: strings.Join([]string{label, label, label, strings.Repeat("z", 61)}, "."), Resource: contracts.ResourceMCP,
			Parts:    []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments},
			Replaced: true,
		},
	}
	n.Facts.EndsAt = timeMax.Add(-time.Nanosecond).In(time.FixedZone("y", -(12*3600 + 59*60)))
	if len(n.Facts.Vendor) != vendorMax {
		t.Fatalf("vendor of %d, want the longest, %d", len(n.Facts.Vendor), vendorMax)
	}
	if err := n.Check(); err != nil {
		t.Fatalf("the largest notice is refused: %v", err)
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > NoticeMax || NoticeMax >= EntryMax {
		t.Fatalf("the largest notice is %d bytes, NoticeMax %d, EntryMax %d", len(b), NoticeMax, EntryMax)
	}
}

// ADR 0041, "Changed after the design review", 3: a notice's window is read from the time it carries, signed with it,
// never from its entry's expiry, which whoever writes the table can change.
func TestE24TheSignedWindow(t *testing.T) {
	n := valid()[0]
	cases := []struct {
		name  string
		now   time.Time
		fresh bool
		send  bool
	}{
		{"queued as it happens", n.At, true, true},
		{"queued AtSkew late", n.At.Add(AtSkew), true, true},
		{"queued past AtSkew", n.At.Add(AtSkew + time.Nanosecond), false, true},
		{"queued AtSkew early", n.At.Add(-AtSkew), true, true},
		{"queued past AtSkew early", n.At.Add(-AtSkew - time.Nanosecond), false, false},
		{"tried at the window's last moment", n.At.Add(RetryFor - time.Nanosecond), false, true},
		{"tried at the window's end", n.At.Add(RetryFor), false, false},
		{"replayed a month later", n.At.AddDate(0, 1, 0), false, false},
	}
	for _, c := range cases {
		if got := n.Fresh(c.now); got != c.fresh {
			t.Errorf("%s: Fresh = %v, want %v", c.name, got, c.fresh)
		}
		if got := n.Sendable(c.now); got != c.send {
			t.Errorf("%s: Sendable = %v, want %v", c.name, got, c.send)
		}
	}
	marked := n.At
	for _, c := range []struct {
		now  time.Time
		hold bool
	}{
		{marked, true}, {marked.Add(KeptFor - time.Nanosecond), true}, {marked.Add(KeptFor), false},
		{marked.Add(-AtSkew), true}, {marked.Add(-AtSkew - time.Nanosecond), false},
	} {
		if got := MarkHolds(marked, c.now); got != c.hold {
			t.Errorf("MarkHolds(marked, marked%+v) = %v, want %v", c.now.Sub(marked), got, c.hold)
		}
	}
	if AttemptFor >= LeaseFor || PassSendsMax != SendsPerSecond*int(RunEvery/time.Second) || ListMax < PassSendsMax {
		t.Fatalf("AttemptFor %v, LeaseFor %v, PassSendsMax %d, ListMax %d", AttemptFor, LeaseFor, PassSendsMax, ListMax)
	}
}
