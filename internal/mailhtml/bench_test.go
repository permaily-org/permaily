package mailhtml

import (
	"context"
	"io"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Test 23: adversarial benchmarks on the pinned x/net/html, with the time
// of one render and the peak of the heap while it runs, for ADR 0026's
// Limits table. Run in a container of the production enclave's size:
//
//	docker run --cpus=1 --memory=2g ... go test ./internal/mailhtml -run '^$' -bench . -benchtime 5x

// peakHeap runs f and says the most the heap held above where it started,
// sampled every 100 microseconds.
func peakHeap(f func()) uint64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		t := time.NewTicker(100 * time.Microsecond)
		defer t.Stop()
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > base.HeapAlloc && m.HeapAlloc-base.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc - base.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	f()
	close(stop)
	wg.Wait()
	return peak.Load()
}

func benchRender(b *testing.B, in Input, want string) {
	b.Helper()
	b.ReportAllocs()
	var peak uint64
	for b.Loop() {
		p := peakHeap(func() {
			st, err := Render(context.Background(), in, io.Discard)
			if err != nil || st.Status != want {
				b.Fatalf("%+v %v", st, err)
			}
		})
		peak = max(peak, p)
	}
	b.ReportMetric(float64(peak)/(1<<20), "peak-MiB")
}

// Item 12.
func BenchmarkAmpersands(b *testing.B) {
	benchRender(b, input(strings.Repeat("&", contracts.MessageHTMLMaxBytes)), contracts.HTMLTooLarge)
}

// Item 13: refused by the token pass.
func BenchmarkBreaks(b *testing.B) {
	benchRender(b, input(strings.Repeat("<br>", contracts.MessageHTMLMaxBytes/4)), contracts.HTMLTooLarge)
}

// Item 13: the amplification case, refused by the token pass.
func BenchmarkAmplification(b *testing.B) {
	benchRender(b, input(amplification(contracts.MessageHTMLMaxBytes)), contracts.HTMLTooLarge)
}

// At the limits that are shown: 50,000 start tags, 256 deep.
func BenchmarkStartTagsAtTheLimit(b *testing.B) {
	benchRender(b, input(strings.Repeat("<br>", contracts.MessageHTMLMaxStartTags)), contracts.HTMLShown)
}

func BenchmarkNestingAtTheLimit(b *testing.B) {
	benchRender(b, input(strings.Repeat("<div>", contracts.MessageHTMLMaxDepth)+"x"), contracts.HTMLShown)
}

// A 2 MiB real-looking newsletter.
func BenchmarkNewsletter2MiB(b *testing.B) {
	benchRender(b, input(realLookingNewsletter(b, contracts.MessageHTMLMaxBytes)), contracts.HTMLShown)
}

// A newsletter of the size real ones have (about 60 KiB).
func BenchmarkNewsletter60KiB(b *testing.B) {
	benchRender(b, input(realLookingNewsletter(b, 60<<10)), contracts.HTMLShown)
}

// The 50-image message: 50 images that together reach the pixel limit and
// the 5 MiB of bytes, sent as cid: parts.
func BenchmarkFiftyImages(b *testing.B) {
	in := input("")
	in.Inline = map[string]InlinePart{}
	var srcs []string
	for i := range 50 {
		w, h := 1024, 640
		if i >= 48 {
			h = 1024
		}
		id := "img" + string(rune('A'+i/26)) + string(rune('a'+i%26))
		data := pngImage(b, w, h, false)
		data = append(data, make([]byte, (100<<10)-len(data))...) // 100 KiB each: 5,000 KiB together
		in.Inline[id] = InlinePart{Type: "image/png", Data: data}
		srcs = append(srcs, "cid:"+id)
	}
	in.HTML = []byte(imgs(srcs...))
	benchRender(b, in, contracts.HTMLShown)
}

// The amplification shape at its most expensive that is still shown. With
// 400 formatting elements it is never shown (they nest past the depth of
// 256), so this one opens 240, then repeats "</div><div>x" to the largest
// size the limits accept (found by bisection): the most work the tree bound
// and the output's limits let through for this shape.
func BenchmarkAmplificationShown(b *testing.B) {
	shape := func(size int) string {
		var s strings.Builder
		s.WriteString("<div>")
		for i := range 240 {
			s.WriteString(`<b id=` + strconv.Itoa(i) + `>`)
		}
		for s.Len() < size-12 {
			s.WriteString("</div><div>x")
		}
		return s.String()
	}
	shown := func(size int) bool {
		st, err := Render(context.Background(), input(shape(size)), io.Discard)
		return err == nil && st.Status == contracts.HTMLShown
	}
	if !shown(0) {
		b.Fatal("the shape is not shown even at its smallest")
	}
	lo, hi := 0, contracts.MessageHTMLMaxBytes
	for hi-lo > 16 {
		if mid := (lo + hi) / 2; shown(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	src := shape(lo)
	p, err := prepare(context.Background(), []byte(src), true)
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("%d bytes, tree bound %d", len(src), p.nodes)
	benchRender(b, input(src), contracts.HTMLShown)
}

// The adversarial CSS of finding 1 of the review and its siblings, without
// a deadline: what each costs when nothing stops it.
func BenchmarkAdversarialCSS(b *testing.B) {
	shapes := adversarialCSS()
	names := make([]string, 0, len(shapes))
	for name := range shapes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b.Run(strings.ReplaceAll(name, " ", "_"), func(b *testing.B) {
			b.ReportAllocs()
			in := input(shapes[name])
			var peak uint64
			for b.Loop() {
				peak = max(peak, peakHeap(func() {
					if _, err := Render(context.Background(), in, io.Discard); err != nil {
						b.Fatal(err)
					}
				}))
			}
			b.ReportMetric(float64(peak)/(1<<20), "peak-MiB")
		})
	}
}

// The second review's shapes and every adversarial family of the bound
// (review2_test.go), each at the largest size the token pass accepts.
func BenchmarkAdversarialFamilies(b *testing.B) {
	families := adversarialFamilies()
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b.Run(strings.ReplaceAll(name, " ", "_"), func(b *testing.B) {
			n := largestShown(families[name])
			if n < 0 {
				b.Skip("refused at its smallest")
			}
			in := input(families[name](n))
			b.ReportAllocs()
			var peak uint64
			for b.Loop() {
				peak = max(peak, peakHeap(func() {
					if _, err := Render(context.Background(), in, io.Discard); err != nil {
						b.Fatal(err)
					}
				}))
			}
			b.ReportMetric(float64(peak)/(1<<20), "peak-MiB")
			b.ReportMetric(float64(len(in.HTML)), "bytes")
		})
	}
}
