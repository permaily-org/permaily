package attestation

import (
	"strings"
	"testing"
	"time"
)

func chainedReceipts(now time.Time) ReceiptsStatus {
	r := ReceiptsStatus{Sealed: true, Chained: true, Chain: ReceiptsChainStrict, Broken: map[string]int64{}, Decisions: "strict", DecisionsPerDay: 50,
		Incidents: &ReceiptsIncidents{Read: now.Add(-5 * time.Minute)},
		Audit:     &ReceiptsAudit{Began: now.Add(-25 * time.Hour), Finished: now.Add(-24 * time.Hour), Catalogue: 4, Chains: 120, Removed: 2, Recovered: 1, Broken: map[string]int64{}}}
	for _, k := range ReceiptsBrokenKinds {
		r.Broken[k], r.Audit.Broken[k] = 0, 0
	}
	return r
}

// L19 (ADR 0037): permaily-verify requires receipts sealed and chained by
// strict links, a finished pass of the audit no older than
// ReceiptsAuditStaleAfter and nothing broken by the audit or by the enclave
// that answered; a receipt linked late (Recovered) is not a deletion. It
// fails on each case F2 and F6 of the design review name: no pass, a stale
// one, one from the future, a kind missing or unknown, a count below zero.
func TestReceiptsCheck(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	ok := chainedReceipts(now)
	if err := ok.Check(now); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		edit func(*ReceiptsStatus)
		want string
	}{
		"not sealed":  {func(r *ReceiptsStatus) { r.Sealed = false }, "not sealed"},
		"not chained": {func(r *ReceiptsStatus) { r.Chained = false }, "not chained"},
		"not strict":  {func(r *ReceiptsStatus) { r.Chain = "plain" }, "not chained"},
		"no pass":     {func(r *ReceiptsStatus) { r.Audit = nil }, "no pass"},
		"stale": {func(r *ReceiptsStatus) {
			r.Audit.Began, r.Audit.Finished = now.Add(-50*time.Hour), now.Add(-ReceiptsAuditStaleAfter-time.Second)
		}, "more than 48h0m0s old"},
		"future":           {func(r *ReceiptsStatus) { r.Audit.Finished = now.Add(time.Second) }, "after now"},
		"order":            {func(r *ReceiptsStatus) { r.Audit.Began = r.Audit.Finished.Add(time.Second) }, "before it began"},
		"chains below":     {func(r *ReceiptsStatus) { r.Audit.Chains = -1 }, "below zero"},
		"removed below":    {func(r *ReceiptsStatus) { r.Audit.Removed = -1 }, "below zero"},
		"recovered below":  {func(r *ReceiptsStatus) { r.Audit.Recovered = -1 }, "below zero"},
		"unlinked below":   {func(r *ReceiptsStatus) { r.Audit.Unlinked = -1 }, "below zero"},
		"expired below":    {func(r *ReceiptsStatus) { r.Audit.Expired = -1 }, "below zero"},
		"no incidents":     {func(r *ReceiptsStatus) { r.Incidents = nil }, "record of incidents of receipts' chains could not be read"},
		"incidents stale":  {func(r *ReceiptsStatus) { r.Incidents.Read = now.Add(-ReceiptsIncidentsStaleAfter - time.Second) }, "was not read in the 30m0s"},
		"incidents future": {func(r *ReceiptsStatus) { r.Incidents.Read = now.Add(time.Second) }, "was not read in the 30m0s"},
		"incident":         {func(r *ReceiptsStatus) { r.Incidents.Recorded = 1 }, "holds 1 entries"},
		"audit found":      {func(r *ReceiptsStatus) { r.Audit.Broken["receipt_missing"] = 2 }, "the audit found receipts or links of the record of what left that the storage deleted or changed: receipt_missing 2"},
		"catalogue":        {func(r *ReceiptsStatus) { r.Audit.Broken["catalogue_lost"] = 1 }, "catalogue_lost 1"},
		"enclave found":    {func(r *ReceiptsStatus) { r.Broken["link_forked"] = 1 }, "the enclave that answered found"},
		"kind missing":     {func(r *ReceiptsStatus) { delete(r.Audit.Broken, "link_prev") }, "does not count link_prev"},
		"enclave counts":   {func(r *ReceiptsStatus) { r.Broken = nil }, "the enclave that answered does not count"},
		"kind unknown":     {func(r *ReceiptsStatus) { r.Broken["link_eaten"] = 0 }, `does not know ("link_eaten")`},
		"count below zero": {func(r *ReceiptsStatus) { r.Audit.Broken["receipt_changed"] = -1 }, "counts receipt_changed below zero"},
	} {
		r := chainedReceipts(now)
		c.edit(&r)
		err := r.Check(now)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A pass just within the bound, a receipt recovered, one unlinked and a
	// link expired pass; so does a record of incidents read just within
	// its bound.
	edge := chainedReceipts(now)
	edge.Audit.Began, edge.Audit.Finished, edge.Audit.Recovered = now.Add(-50*time.Hour), now.Add(-ReceiptsAuditStaleAfter), 9
	edge.Audit.Unlinked, edge.Audit.Expired, edge.Incidents.Read = 2, 3, now.Add(-ReceiptsIncidentsStaleAfter)
	if err := edge.Check(now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(ReceiptsBrokenKinds, " "), "unlinked") {
		t.Fatal("a receipt without a link is counted as a deletion")
	}
}

// R1 (ADR 0037, L18, L19): a deletion found once keeps permaily-verify
// failing. A pass found a receipt missing; the receipt was put back, a
// later pass found nothing, and the enclave that answers restarted, so its
// own counts are zero: the record of incidents still holds the finding,
// and Check still fails.
func TestAnIncidentOutlivesACleanPass(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	found := chainedReceipts(now)
	found.Audit.Broken["receipt_missing"], found.Incidents.Recorded = 1, 1
	if found.Check(now) == nil {
		t.Fatal("a pass that found a receipt missing passed")
	}
	later := now.Add(26 * time.Hour)
	clean := chainedReceipts(later)
	clean.Incidents.Recorded = 1
	if err := clean.Check(later); err == nil || !strings.Contains(err.Error(), "does not take that back") {
		t.Fatalf("a clean pass took a recorded incident back: %v", err)
	}
}
