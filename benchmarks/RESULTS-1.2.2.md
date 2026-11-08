# Macron 1.2.2 / Hermes: repeated AZM comparison

See the raw JSON alongside this report.

Three shared tasks, three repetitions each, alternating first agent by task and repetition. Same endpoint/model alias, 20 rounds, 180-second agent budget and 200-second process limit. Each run gets a fresh home/workspace.

| Agent | Correct / trials | Median wall seconds (all trials) | API errors excluding shape probes |
|---|---:|---:|---:|
| macron | 7/9 | 41.188 | 0 |
| hermes | 8/9 | 100.278 | 1 |

## Individual trials

| Repeat | Task | Agent | Correct | Seconds | Calls | Reported input | Reported output | Usage complete |
|---:|---|---|---|---:|---:|---:|---:|---|
| 1 | python | macron | FAIL | 37.763 | 5 | 38676 | 2042 | True |
| 1 | python | hermes | FAIL | 200.046 | 6 | 22571 | 1800 | False |
| 1 | repair | hermes | PASS | 147.101 | 7 | 34536 | 2405 | True |
| 1 | repair | macron | PASS | 77.458 | 10 | 92774 | 5406 | True |
| 1 | persian | macron | PASS | 15.947 | 5 | 34149 | 715 | True |
| 1 | persian | hermes | PASS | 49.991 | 5 | 20558 | 802 | True |
| 2 | python | hermes | PASS | 126.695 | 6 | 28978 | 2390 | True |
| 2 | python | macron | PASS | 37.154 | 4 | 30165 | 2455 | True |
| 2 | repair | macron | PASS | 69.490 | 9 | 82938 | 5614 | True |
| 2 | repair | hermes | PASS | 192.550 | 9 | 46528 | 2821 | True |
| 2 | persian | hermes | PASS | 100.278 | 6 | 26429 | 1097 | True |
| 2 | persian | macron | PASS | 43.104 | 3 | 19965 | 413 | True |
| 3 | python | macron | FAIL | 95.903 | 7 | 57626 | 3113 | True |
| 3 | python | hermes | PASS | 79.241 | 6 | 26869 | 1603 | True |
| 3 | repair | hermes | PASS | 45.524 | 6 | 27860 | 2055 | True |
| 3 | repair | macron | PASS | 41.188 | 9 | 76469 | 4104 | True |
| 3 | persian | macron | PASS | 8.477 | 3 | 19956 | 527 | True |
| 3 | persian | hermes | PASS | 44.568 | 6 | 26437 | 1124 | True |

## Efficiency on pairs with two correct outputs and complete usage

7 matched task/repetition pairs qualify. All failed attempts remain in the table above.
- macron: input 356416, output 19234, wall 292.818s.
- hermes: input 211326, output 12694, wall 706.707s.
- Macron relative change in input: +68.66%.
- Macron relative change in output: +51.52%.
- Macron relative change in seconds: -58.57%.

## Interpretation and reproduction

Success is determined by independent executable checks, including 40 deterministic randomized interval cases, decimal precision/error checks and exact Persian CSV totals with input preservation. Model-written tests and claims are not the score.

Token figures are upstream-reported usage, including cached input, rather than monetary charges. Missing usage after an API error or a killed process is a lower bound. The small Hermes API-shape probe is recorded but has no model/messages payload; it is excluded from model-error counts. Wall time includes startup, tools, retries and validation. The endpoint may alias azm to another model; server metadata is stored. Sampling, cache and server load are not controlled.

These measurements establish results for these tasks/configurations. They do not measure Telegram reliability, all tools, security containment or unrelated workloads. See the recorded binary SHA-256 and Hermes commit in the raw metadata. The 1.2.2 frozen baseline binary predates Telegram update/UI polish and the wording of an unused voice group description; its CLI execution paths are unchanged. The 1.2.3 binary is recorded separately with its own digest.

Run `python3 benchmarks/compare.py --help` for options. Use `--repetitions 3`, the actual Macron binary and Hermes launcher, and enter the endpoint key at the hidden prompt. Then run this reporter on the JSON. The administratively interrupted earlier run is separately retained and excluded from this suite.
