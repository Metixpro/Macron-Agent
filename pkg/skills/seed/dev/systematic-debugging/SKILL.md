---
name: systematic-debugging
description: "Use when code fails, a test breaks, or a program behaves wrong: find the root cause before fixing."
version: 1.0.0
category: dev
author: Macron (adapted from Hermes Agent / obra superpowers)
license: MIT
---

# Systematic debugging

Random fixes waste time and create new bugs. No fix before the root cause is known.

## 1. Build a feedback loop first
- Reproduce the user's exact symptom with ONE command you can run again and again (`run_code`, `bash_execute`): a failing test, a small script with the failing input, a curl against the endpoint.
- It must fail now and pass only when the bug is fixed. "Doesn't crash" is not a check.
- Can't reproduce? Gather more data (full error, input, versions); don't guess.

## 2. Find the root cause
- Read the whole error and stack trace: file, line, error code. The answer is often there.
- `search_code` for the error text and for every place the failing function is defined and used.
- Check what changed last: `git log --oneline -10`, `git diff`.
- In a pipeline (A → B → C), log what each stage gets and gives; find the first stage whose output is wrong.
- Compare with a working example in the same codebase; list every difference.

## 3. One hypothesis at a time
- Write it down: "X is the cause because Y". Change ONE thing to test it. Run the loop.
- Wrong? Undo the change and form a new hypothesis. Never stack fixes.
- Three failed fixes in a row → stop: the design is the problem; explain that to the user.

## 4. Fix and prove it
- Fix the cause, not the symptom. Keep the change minimal and in the project's style.
- Run the loop (now green), then the whole test suite. Add a test that would have caught it.
- Report: the cause in one sentence, the fix, and the real output that proves it.
