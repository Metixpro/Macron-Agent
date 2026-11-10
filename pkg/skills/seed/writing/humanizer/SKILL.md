---
name: humanizer
description: "Use when asked to humanize, de-AI or naturalize a text (Persian or English), or to rewrite it in a real voice."
version: 1.0.0
category: writing
author: Macron (adapted from Hermes Agent / blader/humanizer)
license: MIT
---

# Humanizer: remove AI writing patterns

## Steps
1. Keep the meaning, facts and the intended tone. If the user gave a sample of their writing, match its sentence length and words.
2. Find and fix the patterns below.
3. Add a pulse: an opinion where it fits, concrete details instead of general claims, varied sentence length.
4. Final pass: "what still sounds like AI here?" Fix it, then answer with only the rewritten text (and a short note if asked).

## English tells
- Inflated significance ("a testament to", "pivotal", "plays a vital role", "in today's fast-paced world").
- AI vocabulary: delve, tapestry, landscape, foster, leverage, robust, seamless, meticulous, embark, realm, navigate (figuratively).
- "Not only X but also Y", "It's not X, it's Y"; everything in threes; synonym cycling.
- Em dashes everywhere, bold in every line, emoji bullets, Title Case Headings.
- Filler and hedging: "It's important to note that", "may potentially", "In conclusion".
- Servile tone: "Great question!", "I hope this helps!", "Let me know if…".

## Persian tells
- کلیشه‌های ترجمه‌ای: «در دنیای امروز»، «نقش حیاتی ایفا می‌کند»، «در این راستا»، «لازم به ذکر است»، «در نهایت می‌توان گفت».
- فعل‌های مجهول و ساختگی: «مورد بررسی قرار گرفت» ← «بررسی شد»؛ «انجام شد» به‌جای فعل اصلی.
- جمله‌های خیلی بلند با «که»های پشت‌سرهم؛ تکرار «همچنین» و «علاوه بر این».
- لحن کتابی وقتی متن قراره خودمونی باشه، یا برعکس؛ نیم‌فاصله غلط («می شود» ← «می‌شود»).
- جمع‌بندی‌های کلی و شعاری ته متن.

## Rules
- Don't add facts that weren't there. Don't drop details the user cares about.
- Rewriting to dodge an AI detector for cheating (exams, graded work) is not the goal; improving real writing is.
