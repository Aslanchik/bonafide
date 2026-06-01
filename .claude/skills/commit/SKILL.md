---
name: commit
description: Create a git commit with context. Use when the user wants to commit changes, stage files, or create a commit message.
allowed-tools: Bash(git add:*), Bash(git status:*), Bash(git commit:*), Bash(git diff:*)
argument-hint: [message]
---

## Context

- Current git status: !`git status`
- Current git diff: !`git diff HEAD 2>/dev/null || git diff --cached`
- Current branch: !`git branch --show-current`
- Recent commits: !`git log --oneline -10 2>/dev/null || echo "(no commits yet)"`

## Your task

Stage all changes and create a single git commit. Do this now — do not ask for confirmation.

1. Run `git add -A` to stage all changes
2. Choose a commit message:
   - If arguments were provided, use: $ARGUMENTS
   - Otherwise, analyze the changes and write one following conventional commits format:
     - `feat:` for new features
     - `fix:` for bug fixes
     - `docs:` for documentation changes
     - `refactor:` for code refactoring
     - `test:` for adding tests
     - `chore:` for maintenance tasks
3. Run `git commit -m "<message>"`
4. Confirm the commit was created
