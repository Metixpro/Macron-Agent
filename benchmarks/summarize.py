"""Print every valid diagnostic attempt; no best-of selection or hidden retries."""
import json
from pathlib import Path
root=Path(__file__).resolve().parent
files=['azm-comparison.json','azm-hermes-python-corrected.json','azm-selected-tools.json','azm-optimized.json','azm-efficient.json','azm-final-binary-smoke.json']
lines=['# AZM functional pilot results','',
 'Model requested: `azm`. The direct probe reported server model `mimo-v2.6-flash`.',
 'Hermes source commit: `ee6db222`. Macron: `1.1.0-dev`.',
 'This is a small pilot with different agent policies; it does not establish overall superiority.', '',
 '| Run | Agent | Task | Valid result | Wall seconds | API calls | Input tokens | Output tokens |',
 '|---|---|---|---|---:|---:|---:|---:|']
for filename in files:
 p=root/filename
 if not p.exists():continue
 data=json.loads(p.read_text())
 for r in data['results']:
  result='PASS' if r['pass'] else 'FAIL'
  if filename=='azm-comparison.json' and r['agent']=='hermes' and r['task']=='python':result='INVALID evaluator; see rerun'
  if r['timeout']:result='TIMEOUT (see API errors)'
  calls=r['calls'];inp=sum(c['usage'].get('prompt_tokens',0) for c in calls);out=sum(c['usage'].get('completion_tokens',0) for c in calls)
  lines.append(f"| {filename} | {r['agent']} | {r['task']} | {result} | {r['wall_seconds']} | {len(calls)} | {inp} | {out} |")
lines+=['', 'Notes:', '',
 '- JSON files contain all recorded attempts; setup failures with zero model calls are excluded from task scoring.',
 '- Initial Hermes interval output used tuples; the original checker wrongly demanded lists. The corrected independent check accepts both, and its rerun passed.',
 '- The first Hermes repair attempt hit a 100-second upstream timeout in the relay and then the process deadline. It is not evidence that Hermes cannot solve accounting tasks.',
 '- The comparison strips only the invalid trailing `data: [DONE]` marker for both agents. Direct Macron success is recorded separately in azm-pilot.json; real Hermes direct-endpoint compatibility was not scored.',
 '- Selected-tools Macron uses six corresponding shell/file tools; default Macron exposes a broader surface. Compare those configurations separately.',
 '- Tokens come from API usage, include cached input, exclude probe calls, and are not billing estimates. Missing usage on error responses is not treated as zero model work.',
 '- Server load, sampling, cache and network latency were uncontrolled. The evaluator-correction rerun temporarily overlapped another task; its latency is not a controlled head-to-head measurement.',
 '- Independent checks verify output behavior, not the model\'s prose claims or full production readiness.',
 '', 'See README.md for the procedure, limitations and reproduction instructions.']
(root/'RESULTS.md').write_text('\n'.join(lines)+'\n')
print('\n'.join(lines[:7]))
