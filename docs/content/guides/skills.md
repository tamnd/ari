---
title: "Skills and slash commands"
description: "Package a repeatable procedure as a skill the ant can invoke by name or you can run as a slash command, with the full body loaded only on use."
weight: 40
---

A skill is a Markdown file that packages a repeatable procedure: cutting a release, adding a changelog entry, running a migration. The ant sees a one-line listing of every skill and loads a skill's full body only when it invokes one, so a repo full of skills does not tax every turn.

## Writing a skill

Put a `SKILL.md` under `.ari/skills/<name>/` with frontmatter and a body:

```markdown
---
name: changelog
description: Add an entry to CHANGELOG.md in the house format
argument-hint: a one-line summary of the change
---

Add a new entry to CHANGELOG.md for this change: $ARGUMENTS

- Put it under an `## Unreleased` heading, creating the file if missing.
- Write it as a single past-tense bullet, plain language.
```

The `name` and `description` are what the ant sees in the listing. The body, with `$ARGUMENTS` filled in, is loaded only when the skill runs.

## Invoking one

You can run a skill as a slash command in the TUI:

```
/changelog Added a time-of-day tag to the greeting
```

The ant can also invoke a skill on its own when the task matches its description. Either way, invoking one skill loads exactly that skill's body and no other, which is the whole reason skills are lazy rather than preloaded: a repo with dozens of them never pays for a body the ant did not ask for.

## Where skills come from

Skills are discovered from your global ari directory and from the `.ari/skills/` of the workspace and its parents, so you can keep personal skills everywhere and ship project skills in the repo. The listing stays under a strict token budget, which is a build gate, so adding skills never quietly inflates the prompt.
