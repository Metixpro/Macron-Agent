---
name: grounded-citations
description: "Use when an answer, report or document needs sources: cite every outside claim and never invent links."
version: 1.0.0
category: research
author: Waze (adapted from Hermes Agent)
license: MIT
---

# Grounded citations

Every claim taken from outside gets an inline number [1] and a "Sources" list at the end.

## Rules
1. Only cite pages you actually read in this job (`web_fetch`, `browser`, `document_reader`). A search snippet is a lead, not a source: open the page.
2. Number sources in the order they are first cited. Same URL → same number.
3. The link in the list must be the exact URL you read. Never build or guess a URL from memory.
4. Quotes must appear word for word in the page you read; otherwise paraphrase and still cite.
5. Facts you know without a source: say so ("from general knowledge") or leave them out of a sourced report.
6. Numbers, dates, prices and names: check them against the source again before answering.
7. Archived copies: cite with their date ("as archived 2026-08-06"); never present a snapshot as current.
8. Disagreeing sources: show both, with their numbers, and say which is more reliable and why.

## Format (Telegram)
Text with [1], [2] after the claim, then:

Sources:
1. Title — site (link)
2. …

For a PDF report, keep the same numbering and put the list at the end.
