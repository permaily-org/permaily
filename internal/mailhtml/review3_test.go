package mailhtml

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The third code review of pull request 5 (ADR 0026, "Changed after PR
// #92's third code review"): each finding's reproduction, written before
// its fix.

// allocatedBy is what f allocated on the heap in all, whatever it gave back:
// the work it did in bytes copied, without a clock.
func allocatedBy(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// joinFamilies are the inputs of finding A and its variants, as a unit
// repeated: text, then something the walk does not keep, so that the text
// on either side ends up in the same parent.
func joinFamilies() map[string]string {
	x := strings.Repeat("x", 28)
	return map[string]string{
		"unknown elements":              x + "<o:p></o:p>",
		"text in unknown elements":      "<o:p>" + x + "</o:p>",
		"comments":                      x + "<!---->",
		"short comments":                x + "<!>",
		"dropped elements":              x + "<script></script>",
		"details":                       x + "<details></details>",
		"entities around unknown ones":  x + "&amp;<o:p></o:p>&lt;",
		"unknown elements in a link":    "<o:p>" + x + "</o:p>",
		"unknown elements with a space": x + " <o:p> </o:p> ",
	}
}

// A: the walk joined the text on either side of an element it unwraps with
// last.Data += s, copying what it had gathered each time: 1,949,610 bytes of
// "x"×28 and "<o:p></o:p>" held 374.7 MiB of heap at the peak and took
// 8.3 s. Text is gathered as pieces and made into one text node once. For
// each family, in Render and in Links: the work (bytes allocated) at twice
// the size is about twice the work (a quadratic walk makes it four times),
// and at the largest size a part may be the peak of the heap stays within a
// render's memory.
func TestReview3ATextIsJoinedInLinearWork(t *testing.T) {
	ctx := context.Background()
	for name, unit := range joinFamilies() {
		wrap := func(n int) string {
			s := strings.Repeat(unit, n)
			if strings.HasSuffix(name, "in a link") {
				s = `<a href="https://a.example/">` + s + "</a>"
			}
			return s
		}
		for _, pass := range []struct {
			name string
			run  func(src string)
		}{
			{"Render", func(src string) { _, _ = Render(ctx, input(src), io.Discard) }},
			{"Links", func(src string) { _, _ = Links(ctx, input(src)) }},
		} {
			const n = 4000
			once := allocatedBy(func() { pass.run(wrap(n)) })
			twice := allocatedBy(func() { pass.run(wrap(2 * n)) })
			if twice > 3*once {
				t.Errorf("%s, %s: %d KiB at %d units, %d KiB at %d: more than linear", name, pass.name, once>>10, n, twice>>10, 2*n)
			}
		}
		// The largest part: memory within a render's, under a generous
		// deadline that only stops work that is not bounded at all.
		// As many units as a part may hold: within its bytes and its start
		// tags (one a unit at most).
		src := wrap(min(contracts.MessageHTMLMaxBytes/len(unit), contracts.MessageHTMLMaxStartTags) - 10)
		for _, pass := range []string{"Render", "Links"} {
			dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			heap, _ := peakMemory(func() {
				if pass == "Render" {
					_, _ = Render(dctx, input(src), io.Discard)
				} else {
					_, _ = Links(dctx, input(src))
				}
			})
			cancel()
			if heap > renderMemory {
				t.Errorf("%s, %s: %d bytes with %.1f MiB of heap, over the %d MiB of a render", name, pass, len(src), float64(heap)/(1<<20), renderMemory>>20)
			}
		}
	}
}
