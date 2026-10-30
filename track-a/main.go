package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
)

type group struct {
	latencies []uint32
	errors    int
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
			parts[i], errs[i] = scan(io.NewSectionReader(f, bounds[i], bounds[i+1]-bounds[i]))
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
		slices.Sort(g.latencies)
		n := len(g.latencies)
		out = append(out, k[:hourLen]...)
		out = append(out, ":00:00Z "...)
		out = append(out, k[hourLen:]...)
		out = append(out, ' ')
		out = strconv.AppendInt(out, int64(n), 10)
		for _, p := range [...]int{50, 95, 99} {
			out = append(out, ' ')
			out = strconv.AppendUint(out, uint64(g.latencies[(p*n+99)/100-1]), 10)
		}
		out = append(out, ' ')
		out = strconv.AppendInt(out, int64(g.errors), 10)
		out = append(out, ' ')
		out = strconv.AppendInt(out, int64(g.errors)*1000000/int64(n), 10)
		out = append(out, '\n')
	}
	return out
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

// scan aggregates every line of r. The key is the 13-byte hour prefix plus the
// endpoint; the prefix is fixed width, so string order on the key equals
// (hour, endpoint-as-bytes) order.
func scan(r io.Reader) (map[string]*group, error) {
	groups := make(map[string]*group)
	var key []byte
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 1<<16)
	for sc.Scan() {
		line := sc.Bytes()
		// ts SP method SP endpoint SP status SP latency
		f1 := bytes.IndexByte(line, ' ')
		f2 := f1 + 1 + bytes.IndexByte(line[f1+1:], ' ')
		f3 := f2 + 1 + bytes.IndexByte(line[f2+1:], ' ')
		f4 := f3 + 1 + bytes.IndexByte(line[f3+1:], ' ')
		endpoint := line[f2+1 : f3]
		status := line[f3+1 : f4]
		latency := parseUint(line[f4+1:])

		key = append(key[:0], line[:hourLen]...)
		key = append(key, endpoint...)
		g := groups[string(key)]
		if g == nil {
			g = &group{}
			groups[string(key)] = g
		}
		g.latencies = append(g.latencies, latency)
		if status[0] == '5' {
			g.errors++
		}
	}
	return groups, sc.Err()
}

func parseUint(b []byte) uint32 {
	var v uint64
	for _, c := range b {
		v = v*10 + uint64(c-'0')
	}
	return uint32(v)
}
