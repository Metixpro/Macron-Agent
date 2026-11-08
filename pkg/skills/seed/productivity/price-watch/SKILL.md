---
name: price-watch
description: "Use when the user wants a product, currency or crypto price tracked, compared, or alerted on a target."
version: 1.0.0
category: productivity
author: Waze (adapted from Hermes Agent)
license: MIT
---

# Price watch

## Read the price now
- Crypto, dollar, toman → `crypto_currency_price`.
- A product page → `web_fetch` (it falls back to readers and the browser on its own). Take the price from the page itself; note the currency, whether it's in stock, the seller and the time.
- Comparing shops: `web_search` for the exact model name (with the storage/color variant), open the top 3–5 shops, compare the same variant. Mention shipping when it changes the winner.

## Watch it over time
- Use `schedule` with `kind: task`, e.g. daily at 10:00: "Check the price of <exact product> at <URL>. Last price: <price> (<date>). Tell the user only if it changed by more than 3% or went under <target>; otherwise say nothing new." Put the last known price in the task text so the next run can compare.
- Confirm the schedule (time, how often, the condition) in one line; the user can cancel it with /reminders.

## Rules
- Never guess or remember a price: every number comes from a page read in this job, with its time.
- If the page can't be read, say so and use another shop; don't report a stale number as current.
