# Track A: performance log

Every step from the reference to your final version, each with the live-board run ID that measured it. Judges check that the run IDs exist and that each change moved the way you say it did.

| Step | Change | Run ID | T (s) | M (MB) | Points |
|---|---|---|---|---|---|
| 0 | The reference, unchanged | | 32.05 | 228 | 0 |
| 1 | Version 1 baseline: parallel Go (up to 6 workers, `bufio.Scanner`, per-worker maps, parallel `slices.Sort` and format). Local run, not yet board-measured | | 0.29 | 82 | |
| 2 | Quickselect for p50/p95/p99 instead of a full sort of each group. Local run, not yet board-measured | | 0.28 | 82 | |
