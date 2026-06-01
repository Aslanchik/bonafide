---
name: new-branch
description: Create and checkout a new git branch with a conventionally named branch based on the context of the task. Use when the user wants to start work on a new feature, fix, or chore and needs a branch.
argument-hint: "[optional: brief description of the work]"
disable-model-invocation: true
allowed-tools: Bash(git *)
---

Create a new git branch and check it out.

## Current state
- Current branch: !`git branch --show-current`
- Recent branches: !`git branch --sort=-committerdate | head -10`

## Your task

1. Based on $ARGUMENTS (or the recent conversation context if no arguments given), choose a branch name following this convention:
   - `feat/<short-description>` — new feature
   - `fix/<short-description>` — bug fix
   - `chore/<short-description>` — tooling, config, cleanup
   - `refactor/<short-description>` — code restructure
   - `test/<short-description>` — tests only
   - Use kebab-case, max ~40 chars, no special characters

2. Run:
   ```bash
   git checkout main && git checkout -b <branch-name>
   ```

3. Confirm the branch was created and tell the user the branch name.
