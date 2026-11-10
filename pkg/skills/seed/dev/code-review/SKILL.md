---
name: code-review
description: "Use when asked to review, audit, clean up or simplify code or a pull request."
version: 1.0.0
category: dev
author: Macron (adapted from Hermes Agent)
license: MIT
---

# Code review

## Order of work
1. Understand the intent: what is this code (or change) supposed to do? Read the README, the entry point and the tests first (`project_tree`, `code_outline`).
2. Run it: build, lint and test (`bash_execute`). A review that never ran the code guesses.
3. Read the changed code top-down, then every caller of what changed (`search_code`).

## What to look for, most important first
1. **Correctness**: wrong logic, off-by-one, unhandled errors, nil/None, empty input, unicode, time zones, concurrency (shared state, races).
2. **Security**: injection (SQL, shell, path), secrets in code, missing auth checks, unsafe deserialization, SSRF.
3. **Data loss**: writes without transactions, partial files (write to a temp file, then rename), deletes without checks.
4. **Performance** only where it matters: N+1 queries, work inside hot loops, unbounded memory.
5. **Simplicity**: dead code, duplicated logic, needless layers, names that lie. Prefer deleting code to adding it.

## Rules
- Every finding: file:line, what is wrong, a realistic input that breaks it, and the fix. No vague "consider improving".
- Separate must-fix from nice-to-have. Say what is good, briefly.
- When asked to fix: small changes with `edit_file`, run the tests again, show the real output.
