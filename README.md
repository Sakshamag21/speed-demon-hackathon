# Track A: Ingest

The task, the input files and the exact output format are in the [spec](https://github.com/hackathon-raptors/speed-demon-2026/blob/main/specs/a-ingest.md).

## Approach

Go, standard library only. The file is split into one byte range per worker (at most 6, matching the 6 P-cores the harness gives us), and each range boundary is moved forward to the next line start, so no line is split or read twice. Each worker scans its range, splits lines with `bytes.IndexByte`, reads the status and latency as integers directly from the bytes, and aggregates into its own map keyed by the 13-byte hour prefix (`YYYY-MM-DDTHH`) plus the endpoint. The hour is never parsed as a date: it is only a fixed-width key, so plain string order on the key is exactly the spec's order (hour, then endpoint as unsigned bytes).

Worker maps are merged into one, the keys are sorted once, and the sorted keys are split into contiguous ranges that workers sort and format in parallel. Percentiles are exact nearest-rank on the sorted latencies of each group; the output is the concatenation of the formatted ranges.

Correctness is checked by comparing the SHA-256 of `summary.txt` against the Python reference's output at tiny, sample and full scale.

## Dependencies

None.

## With another 24 hours

Memory is the main cost left: small per-group latency slices overallocate as they grow, and each worker keeps its own map. I would count per-group sizes first and fill one flat latency array by counting sort, which should cut peak memory by a large factor. After that, a block reader with hand-rolled field parsing in place of `bufio.Scanner`, and a profile-guided look at the merge step.
