package notices

import (
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

var at = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

// valid is a notice of each kind with exactly the facts its kind has.
func valid() []Notice {
	conn := Facts{Vendor: "claude.ai", Resource: contracts.ResourceMCP, Parts: []contracts.Part{contracts.PartSender}, EndsAt: at.AddDate(0, 0, 90)}
	reuse := conn
	reuse.EndsAt = time.Time{}
	replaced := conn
	replaced.Replaced = true
	n := func(k Kind, f Facts) Notice {
		return Notice{Account: "acc_ana2", Kind: k, Key: Key("acc_ana2", k, "x"), At: at, Facts: f}
	}
	return []Notice{
		n(KindConnectionApproved, conn), n(KindConnectionApproved, replaced), n(KindDeviceAdded, conn),
		n(KindConnectionRenewed, conn), n(KindConnectionEnding, conn), n(KindDeviceEndedReuse, reuse),
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
