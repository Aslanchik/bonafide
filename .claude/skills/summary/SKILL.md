---
name: summary
description: Generate a summary of recent work — files changed, functions touched, dependencies added, and why. Use when the user asks for a summary, changelog, or recap of what was done.
---

## Context

- Commits: !`git log --oneline -20`
- Changed files: !`git diff --name-only HEAD~20 HEAD 2>/dev/null || git show --name-only --format="" HEAD`
- Full diff stat: !`git diff --stat $(git rev-list --max-parents=0 HEAD) HEAD 2>/dev/null || echo "(single commit — see changed files above)"`
- Go modules: !`git show HEAD:services/authz/go.mod 2>/dev/null | head -30`
- Python deps: !`git show HEAD:services/control/pyproject.toml 2>/dev/null | grep -A 30 'dependencies'`

## Your task

Produce a structured summary of the work above. Format:

### What was done
Bullet points — one per logical change (not one per commit). Group related changes.

### Major files and functions touched
List each file and the specific functions/sections modified, with a one-line note on what changed.

### Dependencies added
For each new dependency: name, why it was added, what it enables. If none, say so.

Keep it concise. This is a recap, not a tutorial.
