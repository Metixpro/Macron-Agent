# azm matched-model pilot

The comparison runs the actual Macron binary and actual Hermes AIAgent core,
not a simulated Hermes loop. The endpoint key is read with getpass, kept only in
memory in a local forwarding relay, and never written to this directory. Both
agents receive only a dummy relay credential. Do not put a real key in arguments.

Three synthetic task specifications are shared: merge intervals in Python,
repair exact-decimal accounting, and a Persian CSV aggregation instruction.
Each task gets a fresh private directory and home. Both use model `azm`, a
20-round limit and a 180-second agent deadline (200-second process kill limit).
An independent evaluator checks function behavior or exact JSON and input
preservation; prose claiming success is insufficient. The validator assesses
result correctness, not whether all requested model-written tests were run.

Hermes uses its PM-installed dependency environment, terminal/file toolsets,
and disables existing memory, background review and repository context. Macron
uses the one-shot exec workflow with its built-in tool selection and seeded
skills. These are different agent policies, not identical prompts/tool schemas.
Wall time includes process startup; tokens are the upstream API's reported usage
summed across observed completion calls, not estimates or monetary cost.

`azm-pilot.json` records initial diagnostics. Its Hermes attempts had **zero
model calls** due to a dependency-environment selection error in our launch
wrapper. They must NOT be scored as Hermes task failures. The wrapper was fixed
before the comparison. Macron's three direct-endpoint tasks did pass independent
checks in that diagnostic run.

The endpoint advertises `azm`, but its probe response's model field reported
`mimo-v2.6-flash`. This is server metadata, not independently verified identity.
The endpoint also appended `data: [DONE]` after non-streaming JSON. The comparison
uses `--normalize` to strip only that exact invalid trailer for BOTH agents, then
requires valid JSON. It does not modify the model, prompts or tool calls.

Run in the prepared workspace (the Hermes launcher contains this installation's
local paths; change them for a different machine):

```sh
python3 benchmarks/compare.py \
  --endpoint http://HOST:PORT/v1 \
  --normalize \
  --macron /path/to/macron \
  --hermes-python /path/to/hermes_launch.sh \
  --hermes-repo /path/to/hermes-agent \
  --output benchmarks/azm-comparison.json
```

This is one trial per task, with alternating agent order. Upstream cache, load,
random sampling and network latency are not controlled. Output artifacts are
verified before temporary workspaces are deleted; summary logs and API metrics
remain in JSON. It is a small functional pilot, not evidence of overall
superiority, production reliability, security containment or model quality.

Evaluator correction: the initial merge-interval check required nested lists,
but the task did not forbid tuples. That false negative is excluded. The
corrected validator accepts either representation and the Hermes rerun passed;
its solution and tests are stored in `azm-hermes-python-corrected.json`.

The default Macron run advertised substantially more tools than the selected
Hermes terminal/file surface. A follow-up uses Macron's new `--tools` option
with six corresponding shell/file capabilities. It is stored separately; do not
silently combine the default and restricted variants. The new option does not
provide OS isolation.

## Repeated 1.2.2 / 1.2.3 review

`--repetitions 3` repeats all three tasks, alternating agent order by both task
and repetition. Each trial has its own metrics label and clean workspace. The
interval validator adds 40 deterministic generated cases; accounting includes
large exact-decimal values and signed whitespace input. These stronger checks
are not used to rewrite the old pilot scores.

The 1.2.2 baseline uses the default Macron CLI tool discovery and the latest
Hermes source at `e0754412`. Both get the same task prompts, endpoint and limits.
The 1.2.3 CLI context change is measured in a separate follow-up, preserving the
baseline attempts. Comparisons across phases must disclose that scheduling,
network/server load, sampling and cache were not controlled. Failed or timed-out
trials remain in the raw files; incomplete usage is never treated as a full cost.

```sh
python3 benchmarks/compare.py --endpoint http://HOST:PORT/v1 --normalize \
  --macron /path/to/macron --hermes-python benchmarks/hermes_launch.sh \
  --hermes-repo /path/to/hermes-agent --repetitions 3 --output results.json
python3 benchmarks/report_head_to_head.py results.json --output results.md
```

An interrupted preparatory attempt is retained separately and excluded; it was
stopped to apply the requested feature removals before the repeated suite.
