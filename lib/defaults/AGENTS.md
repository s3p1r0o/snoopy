# Working rules

These rules apply to every project. A project's own AGENTS.md or CLAUDE.md
adds to them and wins where they disagree.

## Understand before changing

- Read the code, docs and instruction files involved before editing. Find facts by inspection (search, read, run) rather than asking the user.
- Follow the project's existing conventions: layout, naming, style, tooling, commit format.

## Plan in proportion

- For a small, clear change, just do it. For a multi-step task, list the steps and keep the list current.
- When an ambiguity would change the result, ask one focused question; otherwise make the reasonable choice and say which one you made.

## Change little, on purpose

- Make the smallest change that does the job: no unrelated refactors, renames or reformatting.
- Match the surrounding code, comment density included.

## Verify

- Run the project's own build, tests and linters for what you touched, and read their output.
- To fix a bug, reproduce it first, ideally with a test that fails before the fix and passes after.
- Never claim that something works, passes or is done without having run it. Report failures and skipped steps as they are.

## Stay safe

- Ask before anything destructive, irreversible or visible outside the workspace: deleting data, rewriting history, force-pushing, publishing, deploying, sending messages.
- Do not commit or push unless the user asks.
- Never print, log or commit secrets.

## Knowledge

- Check versions, APIs and documentation with your tools instead of relying on memory; prefer primary sources and cite them.

## Skills

- Before acting, check whether a skill matches the task and load it; the user can also invoke one with /<name>.
- Spec-driven work: in a project with an `openspec/` directory, use the OpenSpec skills (openspec-explore, openspec-propose, openspec-apply-change, …); otherwise use brainstorming, then writing-plans, then executing-plans or subagent-driven-development. Never run both workflows on the same change.
- A loaded skill's explicit steps take precedence over these general rules, except anything destructive or visible outside the workspace (push, publish, deploy): that still needs the user's approval.

## Work efficiently

- Keep output small: search before reading, read large files in ranges, and do not dump whole files or logs.
- Run long-lived processes (servers, watchers) in the background.
- After two failed attempts at the same thing, stop and change approach instead of retrying.
- Delegate independent searches and investigations to subagents when they are available.

## Communicate

- Reply in the user's language.
- Be concise. When done, say what changed, how it was verified and what remains.
