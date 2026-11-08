---
name: skill-authoring
description: "Use when creating or improving a skill with save_skill, so it is short, triggerable and reusable."
version: 1.0.0
category: meta
author: Waze (adapted from Hermes Agent)
license: MIT
---

# Writing a good skill

A skill is a procedure for a KIND of job, not a record of one conversation.

## The description decides everything
- It is the only line the model always sees. Start with "Use when …" and name the concrete trigger: "Use when a Telegram bot must stream live progress." Never vague ("helps with coding").
- At most 200 characters, English.

## The body
- Title, then short sections: steps in order, rules with their reason ("…because the link expires in 30 minutes"), pitfalls, and how to check the result.
- One rule per lesson. Imperative ("Validate the config before reloading"), not a story ("I tried X and then…").
- Name the real tools and exact commands, URLs and parameters that worked.
- Keep it under ~80 lines. Long reference material goes in a file the skill names, not in the body.

## Never
- User data, chat contents, names, secrets, keys, tokens or one-off facts.
- Duplicates: read the existing skill first (`read_skill`) and improve it instead of making a second one.

## When to save
- A hard job needed many steps or a workaround that will come up again.
- A skill you followed was wrong or missing a pitfall: save the corrected full text under the same name (the version goes up by itself).
