---
name: blocked-page-recovery
description: "Use when a fetch fails: 403/429, paywall, WAF, bot wall."
version: 1.1.0
category: web
---

# Reading a page that blocks bots

## Fallback order: stop at the first one that works
1. Plain request: `web_fetch`. On its own it retries a blocked page through a reader service and other readers. A real article in the result means you are done.
2. Still a block page (403/429/503, "Just a moment", a captcha, "enable JavaScript", a near-empty body)? Open it in the real browser (`browser`) when it is available: wait for the content, close the cookie banner, read the text.
3. A saved copy (adapted from Hermes Agent's ladder):
   - Wayback Machine: `web_fetch` `https://archive.org/wayback/available?url=<url>` → open `archived_snapshots.closest.url`.
   - archive.today (often has paywalled news): `https://archive.ph/newest/<url>`, then `.md`, `.li`, `.is` if one is rate-limited.
   - The site's own API or feed: `/feed`, `/rss`, `.json`, `?format=json`, `/api/…` on the same host.
4. Still nothing? Find the same content elsewhere with `web_search`: the exact title in quotes, the AMP or print version, a syndicated copy (news agencies, mirrors), or the author's own post.

## Rules
- Never say you read a page you did not read. If every way failed, say so in one line and answer from the sources you did read, naming them.
- 429 means "slow down": don't hammer the same URL; switch to another source.
- Don't break logins, paywalls or captchas: use legitimate copies (an archive, syndication, the publisher's free version).
- Cite the page you actually read, not the one you wanted. An archived copy is cited with its date and never presented as current (prices, availability, news may have changed).
