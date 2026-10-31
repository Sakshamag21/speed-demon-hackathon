# Track A: performance log

Every step from the reference to your final version, each with the live-board run ID that measured it. Judges check that the run IDs exist and that each change moved the way you say it did.

| Step | Change | Run ID | T (s) | M (MB) | Points |
|---|---|---|---|---|---|
| 0 | The reference, unchanged | | 32.05 | 228 | 0 |
| 1 | Version 1 baseline: parallel Go (up to 6 workers, `bufio.Scanner`, per-worker maps, parallel `slices.Sort` and format). Local run, not yet board-measured | | 0.29 | 82 | |
| 2 | Quickselect for p50/p95/p99 instead of a full sort of each group. Local run, not yet board-measured | | 0.28 | 82 | |
| 3 | Block reader (1 MB reads, partial line carried over) and single-pass fixed-width line parsing in place of `bufio.Scanner` plus `IndexByte` per field. Local run, not yet board-measured; measured against step 2 in the same session: 0.32 s to 0.28 s median | | 0.28 | 88 | |
| 4 | Group lookup: one small open-addressing table per hour (72-hour window from the spec, fallback map outside it), hashing only the endpoint's length and first/last 8 bytes; joined key built once per group. Local run, not yet board-measured; measured against step 3 in the same session: 0.311 s to 0.296 s median, scan alone 400 ms to 345 ms single-threaded | | 0.30 | 94 | |
