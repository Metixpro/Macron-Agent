# Macron: evidence required before claiming superiority

The uploaded engine already has tool calling, delegation, compaction, model
failover, SQLite/FTS, skills, learning, scheduled work and Telegram integration.
Counting advertised tools is not a useful comparison against Hermes.

Priority work and acceptance criteria:

1. Durable tasks: persist intent before effects, recover after kill -9/reboot,
   avoid repeating completed destructive steps, reconcile ambiguous remote results.
   Verify at every crash boundary, including failed database writes.
2. Unified local sessions: interactive CLI, resume/history/search, memory writes,
   schedules, provider failover and plugins through the same application wiring.
   The new one-shot exec interface is a foundation, not completion of this work.
3. Execution policy: explicit full-host versus OS/container isolation, identical
   policy across shell, Python, SSH, browser, plugins and MCP. Test credentials,
   mounts and egress at the real process boundary. Pattern matching is not isolation.
4. Untrusted content and secrets: provenance on retrieved text/tool output,
   destination-scoped credential handling and redacted model/log/trace egress.
   Audit existing third-party media/search defaults before relying on them.
5. Operational recovery: backup/restore drills with live WAL databases, schema
   migration failure, disk-full, provider outage, graceful shutdown and upgrades.
6. Extension lifecycle: versioned plugin contracts, dependency checks, health and
   bounded execution; test bad/crashing plugins and MCP disconnects.
7. Capability validation: document/media/browser/SSH integration on supported
   Linux distributions, explicit optional dependencies and honest skip reports.
8. Compare both projects on the same tasks, hardware and model: completion rate,
   false-success rate, cost, p50/p95 latency, peak RSS, recovery after interruption,
   Persian/English tasks and unauthorized-effect count. Publish reproducible runs.

No production model credentials or Telegram token were supplied for this review.
Real provider quality, Telegram delivery and service restart across a real host
reboot must be validated before a production-readiness claim. Existing tests and
mock integration checks do not substitute for those trials.
