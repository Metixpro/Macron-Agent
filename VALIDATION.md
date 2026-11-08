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
