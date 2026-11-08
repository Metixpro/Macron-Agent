---
name: connect-github
description: "Use when the owner wants you to work with their GitHub: clone, change code, commit, push, pull requests, issues, releases."
version: 1.0.0
category: dev
---

# Work with the owner's GitHub

You have `git` and, once installed, the `gh` CLI. There is no GitHub panel: the owner pastes a token (or says it is already set) and you set it up.

## Connect
1. Check first: `gh auth status` and `git config --global user.name; git config --global user.email`.
2. A token (`ghp_…`, `github_pat_…`): `printf %s 'TOKEN' | gh auth login --with-token` (install `gh` if needed: `apt install gh`, `brew install gh`, or the release tarball). Without `gh`, store it for git only: `git config --global credential.helper store` and write `https://USER:TOKEN@github.com` into `~/.git-credentials` with `chmod 600`.
3. Never put a token inside a remote URL you keep, a commit, a log or the answer. After the login, don't repeat it back.
4. Set `git config --global user.name/user.email` from the owner's GitHub profile (`gh api user`) if they are empty.
5. A token that was pasted in the chat is in the chat history: once everything works, remind the owner to rotate it when they are done.

## Work
- Find the repository: `gh repo list`, `gh repo view OWNER/NAME`. Clone into a folder of the workspace, read the files you will touch, change them, run the tests if there are any.
- `git status` and `git diff` before every commit; commit only what the job changed, with a short imperative message that says what and why. Secrets, `.env` files and keys never get committed.
- Big or risky changes go on a branch and a pull request (`gh pr create --fill`), unless the owner said to push to the main branch.
- When the remote moved on, `git pull --rebase`; when a file changed on both sides and it is not obvious, say so and ask.
- Issues, comments, releases, actions: `gh issue …`, `gh pr …`, `gh release …`, `gh run list`, `gh api`.
- Report with the link of the commit or pull request, in a few lines.
