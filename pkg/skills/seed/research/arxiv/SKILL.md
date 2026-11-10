---
name: arxiv
description: "Use when looking for scientific papers or preprints, or summarizing a paper from arXiv."
version: 1.0.0
category: research
author: Macron (adapted from Hermes Agent)
license: MIT
---

# arXiv papers

## Search
- The arXiv API returns Atom XML: `web_fetch` `http://export.arxiv.org/api/query?search_query=all:<words>&start=0&max_results=10&sortBy=relevance`
  - Fields: `ti:` title, `au:` author, `abs:` abstract, `cat:` category (e.g. `cat:cs.CL`); join with `+AND+`, phrases in `%22…%22`.
  - Newest first: `sortBy=submittedDate&sortOrder=descending`.
- For "the best/most cited papers on X", also `web_search` (Google Scholar, Semantic Scholar) — arXiv has no citation counts.

## Read a paper
- Abstract page: `https://arxiv.org/abs/<id>`; full text: `https://arxiv.org/pdf/<id>` → download with `download_file`, then `document_reader` (outline, search, read pages). Cite pages.
- Prefer the latest version (v2, v3…) unless the user asked for a specific one.

## Summarize
1. One-line claim. 2. Problem and why it matters. 3. Method in plain words. 4. Results with the real numbers and baselines. 5. Limits (what the authors admit and what they skip). 6. Link and arXiv id.
- Never invent results, numbers or authors; say "the paper doesn't say" when it doesn't.
