# Macron 1.2.3 / Hermes: repeated AZM comparison

Raw sources: azm-1.2.2-head-to-head.json (Hermes baseline) and azm-1.2.3-followup.json (Macron follow-up). All original attempts are retained.

Three shared tasks, three repetitions each. Hermes was run in the first phase, alternating with Macron 1.2.2; Macron 1.2.3 was then run in a separate phase. This is a cross-phase comparison, not simultaneous or randomized by final version. Same endpoint/model alias, 20 rounds, 180-second agent budget and 200-second process limit. Each run gets a fresh home/workspace.

| Agent | Correct / trials | Median wall seconds (all trials) | API errors excluding shape probes |
|---|---:|---:|---:|
| macron | 8/9 | 24.823 | 0 |
| hermes | 8/9 | 100.278 | 1 |

## Individual trials

| Repeat | Task | Agent | Correct | Seconds | Calls | Reported input | Reported output | Usage complete |
|---:|---|---|---|---:|---:|---:|---:|---|
| 1 | python | macron | PASS | 26.400 | 6 | 41128 | 2926 | True |
| 1 | repair | macron | PASS | 35.162 | 11 | 67530 | 2798 | True |
| 1 | persian | macron | PASS | 8.648 | 3 | 15332 | 470 | True |
| 2 | python | macron | PASS | 45.056 | 10 | 66193 | 4362 | True |
| 2 | repair | macron | PASS | 24.823 | 7 | 40698 | 2006 | True |
| 2 | persian | macron | PASS | 9.189 | 3 | 15432 | 535 | True |
| 3 | python | macron | FAIL | 23.555 | 5 | 31077 | 2441 | True |
| 3 | repair | macron | PASS | 37.401 | 12 | 75472 | 2885 | True |
| 3 | persian | macron | PASS | 7.655 | 3 | 15344 | 495 | True |
| 1 | python | hermes | FAIL | 200.046 | 6 | 22571 | 1800 | False |
| 1 | repair | hermes | PASS | 147.101 | 7 | 34536 | 2405 | True |
| 1 | persian | hermes | PASS | 49.991 | 5 | 20558 | 802 | True |
| 2 | python | hermes | PASS | 126.695 | 6 | 28978 | 2390 | True |
| 2 | repair | hermes | PASS | 192.550 | 9 | 46528 | 2821 | True |
| 2 | persian | hermes | PASS | 100.278 | 6 | 26429 | 1097 | True |
| 3 | python | hermes | PASS | 79.241 | 6 | 26869 | 1603 | True |
| 3 | repair | hermes | PASS | 45.524 | 6 | 27860 | 2055 | True |
| 3 | persian | hermes | PASS | 44.568 | 6 | 26437 | 1124 | True |

## Efficiency on pairs with two correct outputs and complete usage

7 matched task/repetition pairs qualify. All failed attempts remain in the table above.
- macron: input 296001, output 13551, wall 167.934s.
- hermes: input 211326, output 12694, wall 706.707s.
- Macron relative change in input: +40.07%.
- Macron relative change in output: +6.75%.
- Macron relative change in seconds: -76.24%.

## Interpretation and reproduction

Success is determined by independent executable checks, including 40 deterministic randomized interval cases, decimal precision/error checks and exact Persian CSV totals with input preservation. Model-written tests and claims are not the score.

Token figures are upstream-reported usage, including cached input, rather than monetary charges. Missing usage after an API error or a killed process is a lower bound. The small Hermes API-shape probe is recorded but has no model/messages payload; it is excluded from model-error counts. Wall time includes startup, tools, retries and validation. The endpoint may alias azm to another model; server metadata is stored. Sampling, cache and server load are not controlled.

These measurements establish results for these tasks/configurations. They do not measure Telegram reliability, all tools, security containment or unrelated workloads. See the recorded binary SHA-256 and Hermes commit in the raw metadata. The 1.2.2 frozen baseline binary predates Telegram update/UI polish and the wording of an unused voice group description; its CLI execution paths are unchanged. The 1.2.3 binary is recorded separately with its own digest.

Run `python3 benchmarks/compare.py --help` for options. Use `--repetitions 3`, the actual Macron binary and Hermes launcher, and enter the endpoint key at the hidden prompt. Then run this reporter on the JSON. The administratively interrupted earlier run is separately retained and excluded from this suite.
