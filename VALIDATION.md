# Validation of 1.2.3 (2026-11-08)

- `make check`: formatting, vet, 24 test-bearing Go packages and six installer tests passed.
- Targeted race checks passed for the private-only handlers, removed speech behavior,
  server navigation, updater, CLI context and real skill discovery.
- GitHub CI and release workflows for v1.2.2 and v1.2.3 succeeded, including clean install.
- The real GitHub Release installer and updater downloaded archives through the asset
  API fallback, checked SHA256SUMS, ran preflight and retained the previous executable.
  These checks used temporary prefixes and did not restart a live Telegram bot.
- Callback flows use the Telegram test server; no live bot token was supplied for
  end-to-end Telegram operation. Actual systemd/macOS restart remains outside these checks.
- Eighteen baseline model trials and nine follow-up trials were completed against
  the supplied `azm` endpoint. All attempts and generated artifacts are preserved.
  Results: Macron 1.2.3 8/9, Hermes 8/9; the seven comparable correct pairs show less
  observed wall time but more reported input tokens for Macron. Timing across phases
  is uncontrolled. See [the full review](audits/REVIEW-1.2.3.fa.md) and
  [results](benchmarks/RESULTS-1.2.3.md).
- Saved artifacts were replayed independently with 100 generated interval cases,
  exact Decimal checks, integer CSV totals and input preservation. The replay
  confirms the recorded failure as well as the successes; it does not erase failures.

The older evidence below describes the earlier 1.1.0-dev revision.

Latest tool/dependency optimization evidence: audits/OPTIMIZATION.fa.md and audits/RELEASE.json.

# Validation of 1.1.0-dev

Validated on Linux amd64 with Go 1.26.10 and Python 3.12.14.

- Baseline failures were diagnosed: missing QR dependencies, a downloader fixture
  path under the virtual /workspace prefix, and foreground call assertions affected
  by background learning. Dependencies are now explicit; fixtures and the affected
  foreground tests are isolated without disabling product learning.
- Regression tests fail on the original upload for literal service paths, backup
  self-inclusion and missing-home backups, and pass on this revision.
- The full Go test run passed in 24 test-bearing packages; one package has no tests.
  Optional tests can skip when their external dependencies are unavailable.
- The race detector passed for pkg/cli, pkg/config, pkg/agent and pkg/machine.
- Formatting and go vet passed. Both binaries build without CGO from the ZIP source.
- The real installer successfully built and installed this local source into an
  isolated prefix. Installed version and help commands worked.
- A built-binary smoke test used a local HTTP mock model, executed write_file,
  checked the real file contents and returned the tool result to the model.
- Three installer integration tests passed: broken candidate preservation,
  previous version backup, and resistance to a pre-existing macron.new symlink.

Not validated: real Telegram delivery, production inference providers/model quality,
  browser installation, macOS service operation, a live systemd boot/reboot,
  whole-process sandbox containment or comparative Hermes benchmarks.
No live service was installed on this cloud host, no credentials were supplied,
  and nothing was pushed or published to GitHub.

See SERVER.md for installation/runtime boundaries and ENGINEERING_ROADMAP.md for
  the acceptance criteria needed before broader readiness or superiority claims.

Latest validation: make check passed with private hashed Python environments, Chromium and browser-use enabled. Four core packages passed race checks. Autonomous browser navigation with a scripted model executed successfully (not skipped). Both Python dependency graphs were reproduced from SHA-256 lockfiles and checked for compatibility. Go and Python known-vulnerability scans were clean after fixes. A final release-binary Persian CSV smoke passed against azm.
