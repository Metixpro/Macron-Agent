#!/usr/bin/env python3
"""Summarize recorded trials; never impute missing usage on errors as zero."""
import argparse
import json
import statistics
from pathlib import Path


def metrics(row):
    calls = row['calls']
    # Hermes makes a small API-shape probe without a model/messages payload.
    probes = [c for c in calls if c['status'] == 400 and c['request_bytes'] == 15]
    model_calls = [c for c in calls if c not in probes]
    known = all(c['status'] == 200 and 'prompt_tokens' in c['usage']
                and 'completion_tokens' in c['usage'] for c in model_calls)
    return {
        'calls': len(calls), 'shape_probes': len(probes),
        'input': sum(c['usage'].get('prompt_tokens', 0) for c in model_calls),
        'output': sum(c['usage'].get('completion_tokens', 0) for c in model_calls),
        'cached': sum(c['usage'].get('prompt_tokens_details', {}).get('cached_tokens',
                       c['usage'].get('cache_read_input_tokens', 0)) for c in model_calls),
        'usage_complete': known and not row['timeout'],
        'errors': [c['status'] for c in model_calls if c['status'] != 200],
    }


def report(data):
    rows = data['results']
    version = data.get('metadata', {}).get('macron_version', 'macron 1.2.2').removeprefix('macron ')
    lines = [f'# Macron {version} / Hermes: repeated AZM comparison', '',
             data.get('method_note', 'See the raw JSON alongside this report.'), '',
             data.get('schedule_note', 'Three shared tasks, three repetitions each, alternating first agent by task and repetition.') +
             ' Same endpoint/model alias, 20 rounds, 180-second agent budget and 200-second process limit. '
             'Each run gets a fresh home/workspace.', '',
             '| Agent | Correct / trials | Median wall seconds (all trials) | API errors excluding shape probes |',
             '|---|---:|---:|---:|']
    summary = {}
    for agent in ['macron', 'hermes']:
        rs = [r for r in rows if r['agent'] == agent]
        if not rs:
            continue
        summary[agent] = {
            'passed': sum(r['pass'] for r in rs), 'trials': len(rs),
            'median_wall_seconds': statistics.median(r['wall_seconds'] for r in rs),
            'api_errors': sum(len(metrics(r)['errors']) for r in rs),
        }
        s = summary[agent]
        lines.append(f"| {agent} | {s['passed']}/{s['trials']} | {s['median_wall_seconds']:.3f} | {s['api_errors']} |")
    lines += ['', '## Individual trials', '',
              '| Repeat | Task | Agent | Correct | Seconds | Calls | Reported input | Reported output | Usage complete |',
              '|---:|---|---|---|---:|---:|---:|---:|---|']
    indexed = {}
    for r in rows:
        m = metrics(r)
        indexed[(r['repetition'], r['task'], r['agent'])] = (r, m)
        lines.append(f"| {r['repetition']} | {r['task']} | {r['agent']} | {'PASS' if r['pass'] else 'FAIL'} | "
                     f"{r['wall_seconds']:.3f} | {m['calls']} | {m['input']} | {m['output']} | {m['usage_complete']} |")
    paired = []
    for repetition, task in sorted({(r['repetition'], r['task']) for r in rows}):
        a = indexed.get((repetition, task, 'macron'))
        b = indexed.get((repetition, task, 'hermes'))
        if a and b and a[0]['pass'] and b[0]['pass'] and a[1]['usage_complete'] and b[1]['usage_complete']:
            paired.append((a, b))
    lines += ['', '## Efficiency on pairs with two correct outputs and complete usage', '']
    if paired:
        totals = {agent: {'input': 0, 'output': 0, 'seconds': 0} for agent in ['macron', 'hermes']}
        for a, b in paired:
            for agent, (r, m) in [('macron', a), ('hermes', b)]:
                totals[agent]['input'] += m['input']
                totals[agent]['output'] += m['output']
                totals[agent]['seconds'] += r['wall_seconds']
        summary['matched_correct_complete_pairs'] = {'count': len(paired), 'totals': totals}
        lines.append(f'{len(paired)} matched task/repetition pairs qualify. All failed attempts remain in the table above.')
        for agent, s in totals.items():
            lines.append(f"- {agent}: input {s['input']}, output {s['output']}, wall {s['seconds']:.3f}s.")
        for key in ['input', 'output', 'seconds']:
            baseline = totals['hermes'][key]
            if baseline:
                change = 100 * (totals['macron'][key] / baseline - 1)
                lines.append(f'- Macron relative change in {key}: {change:+.2f}%.')
    else:
        lines.append('No qualifying pairs yet; no token-efficiency comparison can be computed.')
    lines += ['', '## Interpretation and reproduction', '',
              'Success is determined by independent executable checks, including 40 deterministic randomized '
              'interval cases, decimal precision/error checks and exact Persian CSV totals with input preservation. '
              'Model-written tests and claims are not the score.', '',
              'Token figures are upstream-reported usage, including cached input, rather than monetary charges. '
              'Missing usage after an API error or a killed process is a lower bound. The small Hermes API-shape '
              'probe is recorded but has no model/messages payload; it is excluded from model-error counts. '
              'Wall time includes startup, tools, retries and validation. The endpoint may alias azm to another model; '
              'server metadata is stored. Sampling, cache and server load are not controlled.', '',
              'These measurements establish results for these tasks/configurations. They do not measure Telegram '
              'reliability, all tools, security containment or unrelated workloads. See the recorded binary SHA-256 '
              'and Hermes commit in the raw metadata. The 1.2.2 frozen baseline binary predates Telegram update/UI '
              'polish and the wording of an unused voice group description; its CLI execution paths are unchanged. '
              'The 1.2.3 binary is recorded separately with its own digest.', '',
              'Run `python3 benchmarks/compare.py --help` for options. Use `--repetitions 3`, '
              'the actual Macron binary and Hermes launcher, and enter the endpoint key at the hidden prompt. '
              'Then run this reporter on the JSON. The administratively interrupted earlier run is separately '
              'retained and excluded from this suite.']
    return '\n'.join(lines) + '\n', summary


if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('data', type=Path)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    text, summary = report(json.loads(args.data.read_text()))
    args.output.write_text(text)
    print(json.dumps(summary, indent=2))
