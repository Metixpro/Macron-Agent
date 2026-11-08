#!/usr/bin/env python3
"""Replay independent checks on saved task artifacts, without any model/API calls."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import tempfile

SALES = 'city,qty,price\nTehran,2,150\nMashhad,3,80\nTehran,-1,150\nMashhad,1,20\nShiraz,4,25\n'
CHECKS = {
    'python': '''import copy, random
from solution import merge_intervals
rng = random.Random(9137)
for _ in range(100):
    data = [[a,a+rng.randrange(10)] for a in [rng.randrange(-30,30) for _ in range(rng.randrange(25))]]
    before = copy.deepcopy(data)
    expected = []
    for a,b in sorted(data):
        if expected and a<=expected[-1][1]: expected[-1][1] = max(expected[-1][1],b)
        else: expected.append([a,b])
    assert [list(x) for x in merge_intervals(data)] == expected
    assert data == before, 'input was mutated'
assert [list(x) for x in merge_intervals([[1,2],[3,4]])] == [[1,2],[3,4]]
assert [list(x) for x in merge_intervals([[1,2],[2,3]])] == [[1,3]]
try: merge_intervals([[4,1]])
except ValueError: pass
else: raise AssertionError('reversed interval accepted')
''',
    'repair': '''from decimal import Decimal
from ledger import total
for rows, expected in [([], '0'), (['0.1','0.2','', '  ', '-0.05'], '0.25'),
                       (['9999999999999999.99','0.01','-1'], '9999999999999999.00'),
                       ([' +2.50 ', '\\t', '-1.25'], '1.25')]:
    actual = total(rows)
    assert isinstance(actual, Decimal) and actual == Decimal(expected), (rows, actual)
try: total(['bad'])
except ValueError: pass
else: raise AssertionError('malformed number accepted')
''',
    'persian': '''import json
from pathlib import Path
actual = json.loads(Path('result.json').read_text())
assert actual == {'Tehran':150,'Mashhad':260,'Shiraz':100}
assert all(type(value) is int for value in actual.values()), 'totals must be integers'
''',
}


def verify(row):
    with tempfile.TemporaryDirectory(prefix='macron-artifact-replay-') as tmp:
        root = Path(tmp)
        for name, data in row['artifacts'].items():
            path = root / name
            if not path.resolve().is_relative_to(root.resolve()):
                raise ValueError('artifact path escapes replay directory')
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(data)
        if row['task'] == 'persian' and (root / 'sales.csv').read_text() != SALES:
            return False, 'sales.csv changed'
        check = 'import os,sys;sys.path.insert(0,os.getcwd())\n' + CHECKS[row['task']]
        result = subprocess.run([sys.executable, '-I', '-c', check], cwd=root,
                                capture_output=True, text=True, timeout=10)
        return result.returncode == 0, result.stderr.strip().splitlines()[-1:]


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('results', type=Path)
    args = parser.parse_args()
    contradictions = 0
    for row in json.loads(args.results.read_text())['results']:
        try:
            passed, detail = verify(row)
        except (OSError, ValueError, subprocess.TimeoutExpired) as error:
            passed, detail = False, str(error)
        score = passed and row['exit_code'] == 0 and not row['timeout']
        contradictions += bool(row['pass']) != score
        print(row.get('repetition', 1), row['agent'], row['task'],
              'artifact PASS' if passed else 'artifact FAIL',
              'process PASS' if row['exit_code'] == 0 and not row['timeout'] else 'process FAIL', detail)
    raise SystemExit(1 if contradictions else 0)
