#!/usr/bin/env bash
set -e
export HERMES_RUNTIME_DIR=/workspace/hermes-bench/tools
# Use PM's committed venv, rather than its bare bootstrap Python. A fresh
# per-test HERMES_HOME must not redirect dependency selection into an empty home.
bench_python="$(/workspace/macron-python/bin/python -c 'import json; p=json.load(open("/workspace/hermes-bench/home/installs/efbae162fde6d014/facts.json")); print(p["packages"]["venv"]["environment"]+"/bin/python")')"
export PATH="$(dirname "$bench_python"):$PATH"
exec "$bench_python" "$@"
