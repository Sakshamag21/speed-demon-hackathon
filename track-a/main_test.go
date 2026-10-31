package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// reference is the obvious implementation: one map keyed by hour prefix and
// endpoint, latencies sorted with the standard library.
func reference(lines []string) string {
	lat := map[string][]uint32{}
	errs := map[string]int{}
	for _, l := range lines {
		f := strings.Split(l, " ")
		key := f[0][:hourLen] + " " + f[2]
		var v uint32
		fmt.Sscan(f[4], &v)
		lat[key] = append(lat[key], v)
		if f[3][0] == '5' {
			errs[key]++
		}
	}
	keys := make([]string, 0, len(lat))
	for k := range lat {
		keys = append(keys, k)
	}
	// "hour SP endpoint": the hour is fixed width, so split on the first space.
	slices.SortFunc(keys, func(a, b string) int {
		return strings.Compare(a[:hourLen]+a[hourLen+1:], b[:hourLen]+b[hourLen+1:])
	})
	var sb strings.Builder
	for _, k := range keys {
		v := lat[k]
		slices.Sort(v)
		n := len(v)
		pc := func(p int) uint32 { return v[(p*n+99)/100-1] }
		fmt.Fprintf(&sb, "%s:00:00Z %s %d %d %d %d %d %d\n", k[:hourLen], k[hourLen+1:], n,
			pc(50), pc(95), pc(99), errs[k], errs[k]*1000000/n)
	}
	return sb.String()
}

func runOn(t testing.TB, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	in := ""
	if len(lines) > 0 {
		in = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "requests.log"), []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func line(ts, endpoint string, status, latency int) string {
	return fmt.Sprintf("%s GET %s %d %d", ts, endpoint, status, latency)
}

func TestGrouping(t *testing.T) {
	const (
		h0 = "2026-10-30T18:"
		h1 = "2026-10-30T19:"
		hn = "2026-11-02T17:"
	)
	tail := "00:00.000000Z"
	lines := []string{
		line(h0+tail, "/a", 200, 5),
		line(h0+"59:59.999999Z", "/a", 503, 9), // same hour, same endpoint: repeated key
		line(h0+tail, "/a", 200, 1),
		line(h1+tail, "/a", 200, 7), // next hour, same endpoint
		line(h0+tail, "/a/b", 500, 3),
		line(h0+tail, "/b", 404, 2),
		line(h0+tail, "/v2/rank100", 200, 4), // byte order: rank100 < rank20
		line(h0+tail, "/v2/rank20", 200, 4),
		line(h0+tail, "/A", 200, 4), // upper case sorts before lower case
		line(h0+tail, "/a_b", 200, 4),
		line(h0+tail, "/a.b", 200, 4),
		line(h0+tail, "/a{id}", 599, 4),
		line(hn+tail, "/a", 200, 6),                                // last window hour
		line("2026-10-30T17:59:59.999999Z", "/a", 500, 11),         // just before the window
		line("2026-11-02T18:00:00.000000Z", "/a", 500, 12),         // just after the window
		line("2026-11-02T18:30:00.000000Z", "/a", 200, 13),         // after window, same hour as above
		line("2025-10-30T18:00:00.000000Z", "/a", 200, 14),         // other year
		line("2026-12-01T00:00:00.000000Z", "/zzz", 200, 15),       // other month
		line("2026-10-30T17:00:00.000000Z", "/a", 200, 4294967295), // max latency
	}
	if got, want := runOn(t, lines), reference(lines); got != want {
		t.Fatalf("output differs\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRandomEquivalence(t *testing.T) {
	endpoints := []string{"/a", "/a/b", "/a/b/c", "/a/{id}", "/A", "/v1/x.y", "/v1/x_y", "/v1/x-y", "/v2/rank100", "/v2/rank20", "/z:q"}
	hours := []string{
		"2026-10-30T17", "2026-10-30T18", "2026-10-30T23", "2026-10-31T00", "2026-10-31T12",
		"2026-11-01T23", "2026-11-02T00", "2026-11-02T17", "2026-11-02T18", "2026-12-31T23", "2027-01-01T00",
	}
	r := rand.New(rand.NewSource(7))
	for it := 0; it < 50; it++ {
		n := r.Intn(3000)
		lines := make([]string, n)
		for i := range lines {
			ts := fmt.Sprintf("%s:%02d:%02d.%06dZ", hours[r.Intn(len(hours))], r.Intn(60), r.Intn(60), r.Intn(1000000))
			status := []int{200, 204, 301, 404, 500, 502, 503, 599}[r.Intn(8)]
			lines[i] = line(ts, endpoints[r.Intn(len(endpoints))], status, r.Intn(50))
		}
		if got, want := runOn(t, lines), reference(lines); got != want {
			t.Fatalf("iteration %d: output differs", it)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	if got := runOn(t, nil); got != "" {
		t.Fatalf("want empty output, got %q", got)
	}
}

// BenchmarkRun runs the whole program on the real log when it is present
// (REQUESTS_DIR, or the repo root next to track-a).
func BenchmarkRun(b *testing.B) {
	dir := os.Getenv("REQUESTS_DIR")
	if dir == "" {
		dir = ".."
	}
	if _, err := os.Stat(filepath.Join(dir, "requests.log")); err != nil {
		b.Skip("no requests.log")
	}
	out := b.TempDir()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := run(dir, out); err != nil {
			b.Fatal(err)
		}
	}
}

func TestPercentiles(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for it := 0; it < 20000; it++ {
		n := 1 + r.Intn(300)
		if it%500 == 0 {
			n = 100000
		}
		mod := []uint32{1, 2, 5, 1000, 1 << 31}[r.Intn(5)]
		a := make([]uint32, n)
		for i := range a {
			a[i] = uint32(r.Uint64()) % mod
		}
		switch it % 4 {
		case 1:
			slices.Sort(a)
		case 2:
			slices.Sort(a)
			slices.Reverse(a)
		}
		s := slices.Clone(a)
		slices.Sort(s)
		w50, w95, w99 := s[(50*n+99)/100-1], s[(95*n+99)/100-1], s[(99*n+99)/100-1]
		if g50, g95, g99 := percentiles(a); g50 != w50 || g95 != w95 || g99 != w99 {
			t.Fatalf("it %d n %d: got %d %d %d, want %d %d %d", it, n, g50, g95, g99, w50, w95, w99)
		}
	}
}

func TestSelectK(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	for it := 0; it < 5000; it++ {
		n := 1 + r.Intn(200)
		a := make([]uint32, n)
		for i := range a {
			a[i] = uint32(r.Intn(20))
		}
		k := r.Intn(n)
		s := slices.Clone(a)
		slices.Sort(s)
		selectK(a, k)
		if a[k] != s[k] {
			t.Fatalf("it %d: a[%d]=%d, want %d", it, k, a[k], s[k])
		}
		for i, v := range a {
			if (i < k && v > a[k]) || (i > k && v < a[k]) {
				t.Fatalf("it %d: partition property broken at %d", it, i)
			}
		}
	}
}
