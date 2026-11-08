# AZM functional pilot results

Model requested: `azm`. The direct probe reported server model `mimo-v2.6-flash`.
Hermes source commit: `ee6db222`. Macron: `1.1.0-dev`.
This is a small pilot with different agent policies; it does not establish overall superiority.

| Run | Agent | Task | Valid result | Wall seconds | API calls | Input tokens | Output tokens |
|---|---|---|---|---:|---:|---:|---:|
| azm-comparison.json | macron | python | PASS | 21.386 | 4 | 76793 | 1967 |
| azm-comparison.json | hermes | python | INVALID evaluator; see rerun | 73.121 | 7 | 33902 | 2067 |
| azm-comparison.json | hermes | repair | TIMEOUT (see API errors) | 200.128 | 8 | 28479 | 2348 |
| azm-comparison.json | macron | repair | PASS | 72.733 | 9 | 174973 | 2447 |
| azm-comparison.json | macron | persian | PASS | 19.922 | 3 | 54206 | 467 |
| azm-comparison.json | hermes | persian | PASS | 51.48 | 5 | 20098 | 660 |
| azm-hermes-python-corrected.json | hermes | python | PASS | 120.078 | 6 | 29434 | 2281 |
| azm-selected-tools.json | macron | python | PASS | 48.215 | 6 | 59272 | 4018 |
| azm-selected-tools.json | hermes | repair | PASS | 117.976 | 7 | 31896 | 2155 |
| azm-selected-tools.json | macron | repair | PASS | 113.361 | 9 | 93181 | 3660 |
| azm-selected-tools.json | macron | persian | PASS | 45.734 | 3 | 25898 | 400 |
| azm-optimized.json | macron | python | PASS | 69.796 | 11 | 110492 | 6761 |
| azm-optimized.json | macron | repair | PASS | 35.546 | 9 | 83812 | 2564 |
| azm-optimized.json | macron | persian | PASS | 7.115 | 3 | 29180 | 507 |
| azm-efficient.json | macron | python | PASS | 18.962 | 5 | 36449 | 1874 |
| azm-efficient.json | macron | repair | PASS | 39.232 | 6 | 42074 | 2037 |
| azm-efficient.json | macron | persian | PASS | 9.071 | 3 | 20015 | 410 |
| azm-final-binary-smoke.json | macron | persian | PASS | 9.749 | 4 | 26819 | 488 |

Notes:

- JSON files contain all recorded attempts; setup failures with zero model calls are excluded from task scoring.
- Initial Hermes interval output used tuples; the original checker wrongly demanded lists. The corrected independent check accepts both, and its rerun passed.
- The first Hermes repair attempt hit a 100-second upstream timeout in the relay and then the process deadline. It is not evidence that Hermes cannot solve accounting tasks.
- The comparison strips only the invalid trailing `data: [DONE]` marker for both agents. Direct Macron success is recorded separately in azm-pilot.json; real Hermes direct-endpoint compatibility was not scored.
- Selected-tools Macron uses six corresponding shell/file tools; default Macron exposes a broader surface. Compare those configurations separately.
- Tokens come from API usage, include cached input, exclude probe calls, and are not billing estimates. Missing usage on error responses is not treated as zero model work.
- Server load, sampling, cache and network latency were uncontrolled. The evaluator-correction rerun temporarily overlapped another task; its latency is not a controlled head-to-head measurement.
- Independent checks verify output behavior, not the model's prose claims or full production readiness.

See README.md for the procedure, limitations and reproduction instructions.
