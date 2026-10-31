package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type group struct {
	latencies []uint32
	errors    int
	key       string // hour prefix + endpoint; set for groups in an endpointTable
}

func main() {
	dataDir := flag.String("data", "/data", "directory holding requests.log")
	outDir := flag.String("out", "/out", "directory to write summary.txt into")
	flag.Parse()
	if err := run(*dataDir, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// hourLen is the length of "YYYY-MM-DDTHH", the hour prefix of a timestamp.
const hourLen = 13

// blockSize is how much of its range each worker reads per call.
const blockSize = 1 << 20

func run(dataDir, outDir string) error {
	f, err := os.Open(filepath.Join(dataDir, "requests.log"))
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}

	workers := min(runtime.GOMAXPROCS(0), 6)
	bounds := make([]int64, workers+1)
	for i := 1; i < workers; i++ {
		bounds[i] = lineStart(f, st.Size()*int64(i)/int64(workers), st.Size())
	}
	bounds[workers] = st.Size()

	parts := make([]map[string]*group, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parts[i], errs[i] = scan(io.NewSectionReader(f, bounds[i], bounds[i+1]-bounds[i]), blockSize)
		}()
	}
	wg.Wait()
	groups := parts[0]
	for i, p := range parts {
		if errs[i] != nil {
			return errs[i]
		}
		if i == 0 {
			continue
		}
		for k, g := range p {
			if dst := groups[k]; dst == nil {
				groups[k] = g
			} else {
				dst.latencies = append(dst.latencies, g.latencies...)
				dst.errors += g.errors
			}
		}
	}

	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	// Each worker sorts and formats a contiguous range of the sorted keys.
	chunks := make([][]byte, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lo, hi := len(keys)*i/workers, len(keys)*(i+1)/workers
			chunks[i] = format(keys[lo:hi], groups)
		}()
	}
	wg.Wait()

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "summary.txt"), slices.Concat(chunks...), 0o644)
}

func format(keys []string, groups map[string]*group) []byte {
	out := make([]byte, 0, 80*len(keys))
	for _, k := range keys {
		g := groups[k]
		n := len(g.latencies)
		p50, p95, p99 := percentiles(g.latencies)
		out = append(out, k[:hourLen]...)
		out = append(out, ":00:00Z "...)
		out = append(out, k[hourLen:]...)
		out = append(out, ' ')
		out = strconv.AppendInt(out, int64(n), 10)
		for _, v := range [...]uint32{p50, p95, p99} {
			out = append(out, ' ')
			out = strconv.AppendUint(out, uint64(v), 10)
		}
		out = append(out, ' ')
		out = strconv.AppendInt(out, int64(g.errors), 10)
		out = append(out, ' ')
		out = strconv.AppendInt(out, int64(g.errors)*1000000/int64(n), 10)
		out = append(out, '\n')
	}
	return out
}

// percentiles returns the nearest-rank p50, p95 and p99 of a, partially
// reordering it. The three ranks are non-decreasing, so each selection only
// has to look right of the previous one: selectK leaves everything before the
// selected element no larger and everything after it no smaller.
func percentiles(a []uint32) (p50, p95, p99 uint32) {
	n := len(a)
	k50, k95, k99 := (50*n+99)/100-1, (95*n+99)/100-1, (99*n+99)/100-1
	selectK(a, k50)
	if k95 > k50 {
		selectK(a[k50+1:], k95-k50-1)
	}
	if k99 > k95 {
		selectK(a[k95+1:], k99-k95-1)
	}
	return a[k50], a[k95], a[k99]
}
func selectK(a []uint32, k int) {
	for len(a) > 16 {
		lo, mid, hi := 0, len(a)/2, len(a)-1
		if a[mid] < a[lo] {
			a[mid], a[lo] = a[lo], a[mid]
		}
		if a[hi] < a[lo] {
			a[hi], a[lo] = a[lo], a[hi]
		}
		if a[hi] < a[mid] {
			a[hi], a[mid] = a[mid], a[hi]
		}
		pivot := a[mid]

		// Invariant: a[:lt] < pivot, a[lt:i] == pivot, a[gt+1:] > pivot.
		lt, i, gt := 0, 0, len(a)-1
		for i <= gt {
			switch v := a[i]; {
			case v < pivot:
				a[lt], a[i] = a[i], a[lt]
				lt++
				i++
			case v > pivot:
				a[i], a[gt] = a[gt], a[i]
				gt--
			default:
				i++
			}
		}
		switch {
		case k < lt:
			a = a[:lt]
		case k > gt:
			a, k = a[gt+1:], k-(gt+1)
		default:
			return // k lies in the run equal to the pivot
		}
	}
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i
		for ; j > 0 && a[j-1] > v; j-- {
			a[j] = a[j-1]
		}
		a[j] = v
	}
}

// lineStart returns the offset of the first line that begins at or after pos.
func lineStart(f *os.File, pos, size int64) int64 {
	var buf [256]byte
	for pos > 0 && pos < size {
		n, _ := f.ReadAt(buf[:], pos-1)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			return pos - 1 + int64(i) + 1
		}
		if n == 0 {
			break
		}
		pos += int64(n)
	}
	return min(pos, size)
}

// numHours is the span of the input: the spec fixes every timestamp between
// 2026-10-30T18 and 2026-11-02T17 inclusive, 72 hours.
const numHours = 72

// hourIndex maps the timestamp's hour to 0..numHours-1 (2026-10-30T18 is 0),
// or -1 if it falls outside that window. It reads the digits directly, so it
// is injective on real dates and never gives an in-window index to another
// hour.
func hourIndex(ts []byte) int {
	if string(ts[:5]) != "2026-" || ts[5] != '1' {
		return -1
	}
	day := int(ts[8]-'0')*10 + int(ts[9]-'0')
	var d int
	switch ts[6] {
	case '0': // October
		d = day - 30
	case '1': // November
		d = day + 1
	default:
		return -1
	}
	h := d*24 + int(ts[11]-'0')*10 + int(ts[12]-'0') - 18
	if h < 0 || h >= numHours {
		return -1
	}
	return h
}

// endpointTable maps endpoint bytes to a group with open addressing and
// linear probing. It exists because the per-line lookup dominated the scan:
// its hash reads only the length and the first and last 8 bytes, and a hit is
// confirmed by comparing the full endpoint, so collisions cost a probe but
// never merge two endpoints.
type endpointTable struct {
	slots []*group
	shift uint // 64 - log2(len(slots))
	n     int
}

func (t *endpointTable) init() {
	t.slots = make([]*group, 256)
	t.shift = 64 - 8
}

func endpointHash(b []byte) uint64 {
	n := len(b)
	var lo, hi uint64
	if n >= 8 {
		lo = binary.LittleEndian.Uint64(b)
		hi = binary.LittleEndian.Uint64(b[n-8:])
	} else {
		for _, c := range b {
			lo = lo<<8 | uint64(c)
		}
	}
	h := (lo ^ uint64(n)) * 0x9e3779b97f4a7c15
	h ^= hi * 0xff51afd7ed558ccd
	return h ^ h>>29
}

// get returns the group for endpoint, adding an empty one keyed by hour +
// endpoint if it is new. Every endpoint in one table shares the same hour.
func (t *endpointTable) get(hour, endpoint []byte) *group {
	mask := len(t.slots) - 1
	for i := int(endpointHash(endpoint) >> t.shift); ; i = (i + 1) & mask {
		g := t.slots[i]
		if g == nil {
			var key strings.Builder // one allocation for the joined key
			key.Grow(len(hour) + len(endpoint))
			key.Write(hour)
			key.Write(endpoint)
			g = &group{key: key.String()}
			t.slots[i] = g
			if t.n++; t.n*2 > len(t.slots) {
				t.grow()
			}
			return g
		}
		if g.key[hourLen:] == string(endpoint) {
			return g
		}
	}
}

func (t *endpointTable) grow() {
	old := t.slots
	t.slots = make([]*group, 2*len(old))
	t.shift--
	mask := len(t.slots) - 1
	for _, g := range old {
		if g == nil {
			continue
		}
		i := int(endpointHash([]byte(g.key[hourLen:])) >> t.shift)
		for t.slots[i] != nil {
			i = (i + 1) & mask
		}
		t.slots[i] = g
	}
}

// scan aggregates every line of r. The result is keyed by the 13-byte hour
// prefix plus the endpoint; the prefix is fixed width, so string order on the
// key equals (hour, endpoint-as-bytes) order.
//
// While scanning, hours inside the spec's window use one small endpointTable
// per hour keyed by the endpoint bytes alone, so no combined key is built and
// the hot table stays cache-sized. Other hours fall back to a map on the
// combined key.
//
// It reads r in blocks of blockSize and parses the complete lines of each
// block in place; a partial last line is carried to the front of the next
// block, and the buffer doubles if one line does not fit.
func scan(r io.Reader, blockSize int) (map[string]*group, error) {
	groups := make(map[string]*group)
	var byHour [numHours]endpointTable
	var key []byte
	buf := make([]byte, blockSize)
	rem := 0
	for {
		n, err := io.ReadFull(r, buf[rem:])
		eof := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !eof {
			return nil, err
		}
		data := buf[:rem+n]
		end := bytes.LastIndexByte(data, '\n') + 1
		if eof && end < len(data) {
			// The spec ends every line with LF; tolerate a missing last one.
			data = append(data, '\n')
			end = len(data)
		}
		if end == 0 && !eof {
			buf = append(buf, make([]byte, len(buf))...)
			rem = len(data)
			continue
		}

		lines := data[:end]
		for p := 0; p < len(lines); {
			// ts(27) SP method(3-7) SP endpoint SP status(3) SP latency LF
			i := p + 31
			for lines[i] != ' ' {
				i++
			}
			ep := i + 1
			j := ep + bytes.IndexByte(lines[ep:], ' ')
			status := lines[j+1]
			k := j + 5
			var latency uint32
			for c := lines[k]; c != '\n'; c = lines[k] {
				latency = latency*10 + uint32(c-'0')
				k++
			}

			var g *group
			if h := hourIndex(lines[p:]); h >= 0 {
				t := &byHour[h]
				if t.slots == nil {
					t.init()
				}
				g = t.get(lines[p:p+hourLen], lines[ep:j])
			} else {
				key = append(key[:0], lines[p:p+hourLen]...)
				key = append(key, lines[ep:j]...)
				if g = groups[string(key)]; g == nil {
					g = &group{}
					groups[string(key)] = g
				}
			}
			g.latencies = append(g.latencies, latency)
			if status == '5' {
				g.errors++
			}
			p = k + 1
		}

		if eof {
			for h := range byHour {
				for _, g := range byHour[h].slots {
					if g != nil {
						groups[g.key] = g
					}
				}
			}
			return groups, nil
		}
		rem = copy(buf, data[end:])
	}
}
