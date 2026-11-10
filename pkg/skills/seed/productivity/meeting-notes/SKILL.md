---
name: meeting-notes
description: "Use when turning a meeting, call, voice note, chat log or long document into notes, decisions and action items."
version: 1.0.0
category: productivity
author: Macron (adapted from Hermes Agent)
license: MIT
---

# Meeting notes and action items

## Input
- A document or PDF → `document_reader` (long) or `read_file`; a pasted chat → as is.
- A voice note or recording: ask the user for a written transcript.

## Output (in the user's language)
1. **Summary**: 3–5 lines: what it was about and the outcome.
2. **Decisions**: one line each, with who decided when it's clear.
3. **Action items**: `- [ ] task — owner — due date` (write "owner?" / "date?" when unknown; never invent them).
4. **Open questions**: what was left unresolved.
5. **Key numbers and dates** mentioned.

## Rules
- Only what was said. Don't add tasks nobody agreed to.
- Keep names exactly as written. Keep it short: notes are for people who weren't there.
- Offer to set reminders for the dated action items (`schedule`), or to make a PDF.
