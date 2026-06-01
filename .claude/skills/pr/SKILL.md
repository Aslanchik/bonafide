---
name: pr
description: Run tests and vetting, then create a GitHub pull request. Use when the user wants to open a PR, submit changes for review, or push a branch.
allowed-tools: Bash(git *), Bash(go vet *), Bash(go test *), Bash(go build *), Bash(uv *), Bash(pytest *), Bash(ruff *), Bash(gh *)
---

## Context

- Current branch: !`git branch --show-current`
- Commits to be included: !`git log main...HEAD --oneline 2>/dev/null || git log --oneline -5 2>/dev/null || echo "(no commits yet)"`
- Git status: !`git status --short`
- Diff stat: !`git diff --stat main...HEAD 2>/dev/null || echo "(no diff)"`

## Your task

Follow these steps in order. Stop and report if any step fails.

### 1. Go vet (if Go files changed)

```bash
go vet ./...
```

Run from each Go module root (`services/authz`). Skip if no Go files changed.

### 2. Go tests

```bash
go test ./...
```

All tests must pass. Skip if no Go files changed.

### 3. Go build

```bash
go build ./...
```

Must compile cleanly. Skip if no Go files changed.

### 4. Python lint (if Python files changed)

```bash
ruff check .
```

Run from each Python project root (`services/control`, `sdks/agent-py`, `sdks/resource-py`, `apps/*`). Skip if no Python files changed.

### 5. Python tests

```bash
pytest
```

All tests must pass. Skip if no Python files changed.

### 6. Docs check

Look at the diff stat above. If any of the following areas changed, verify the corresponding doc has been updated in this branch too. If it hasn't, **stop and tell the user before continuing**.

| Changed area | Required doc update |
|---|---|
| Token claims, exchange request/response, scope grammar, audit event shape (anything wire-format) | `CONTRACT.md` — the section corresponding to the change |
| Service topology, trust topology, TTL budget, container set | `DESIGN.md` — the relevant section |
| Spec workflow, safety constraints, stack pins, working agreements | `CLAUDE.md` |
| New slice introduced, slice scope changed | `specs/<slice>/requirements.md` exists and is reviewed |
| `services/authz/internal/exchange/act_chain.go` | Tests against `CONTRACT.md` §6.1 examples updated in the same commit |
| New diagrams needed for architecture, identity flow, or trust chain | `docs/architecture.md`, `docs/identity-flow.md`, `docs/trust-chain.md` |

If none of these areas were touched, continue to the next step.

### 7. Stage and commit any uncommitted changes

If `git status` shows uncommitted changes, stage and commit them now using a conventional commit message.

### 8. Push the branch

```bash
git push -u origin $(git branch --show-current)
```

### 9. Create the pull request

Use the context above to write the PR title and body. The body should read like a work summary:

```
## What was done
- <one bullet per logical change, grouped by area>

## Files and packages touched
- <package>: <what changed>

## Dependencies added
- <module>: <why it was added> (or "None")

## CONTRACT.md changes
- <which sections changed and why> (or "None — no wire-format changes")

## Testing
- Go vet: passed / N/A
- Go tests: passed (N tests) / N/A
- Go build: clean / N/A
- Python lint: passed / N/A
- Python tests: passed (N tests) / N/A
- Smoke: passed / not applicable to this slice
```

Then run:
```bash
gh pr create --title "<title>" --body "<body>"
```

Return the PR URL when done.

### 10. Switch back to main

```bash
git checkout main
```
