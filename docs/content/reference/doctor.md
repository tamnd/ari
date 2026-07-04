---
title: "ari doctor"
description: "The checks doctor runs, which ones --fix repairs, and the exit-code contract for wiring it into CI."
weight: 30
---

`ari doctor` audits the nest, the config, and any listening surface for the mistakes that turn a coding agent into someone else's remote shell. It is meant to be run by hand after setup and wired into a pre-merge check.

```bash
ari doctor          # report
ari doctor --fix    # apply the safe repairs
ari doctor --audit  # deeper integrity checks
```

## The checks

Doctor runs its checks in a fixed order and reports each as ok, a warning, or critical.

| Check | What it looks for | Severity when it fails |
| ----- | ----------------- | ---------------------- |
| Nest permissions | The credentials directory is `0700` and its files are `0600`, not group or world readable. | critical |
| Secrets in config | A literal API key, token, secret, or password written into a config file instead of an `${ENV}` reference. | critical |
| Config health | The config loads, and any warning from an unknown or shadowed setting. | critical on load error, warning otherwise |
| Permission mode | The standing permission default is not `full-auto`. | warning |
| Local config gitignore | In a git repo with a project `.ari/`, that `.ari/local.toml` is gitignored. | warning |
| Bind status | Any listening surface is configured safely. In M0 there is no listener, so this is always ok. | ok |
| Journal continuity | The session journal has no sequence gaps. | critical on a gap |

The secrets check never logs the value it found. It names the file and the setting, so the finding tells you where to fix it without reprinting the secret.

## What --fix repairs

`--fix` applies the repairs that are unambiguously safe and leaves the judgment calls to you:

- Tightens a loose credentials directory or file back to `0700` / `0600`.
- Adds the `.ari/local.toml` line to the repo's `.gitignore`.

It does not touch a literal secret in a config file, because the right fix is to move the value into your environment and replace it with a reference, which only you can do. After applying, doctor reruns the checks so the report reflects the repaired state.

## Exit codes

Doctor uses its own contract, separate from the run exit codes, so a CI gate can branch on the audit result:

| Code | Meaning |
| ---- | ------- |
| 0 | clean |
| 1 | warnings only |
| 2 | at least one critical finding |
| 3 | doctor could not run |

A pre-merge job that runs `ari doctor` fails on a committed secret or a loose credential file before it reaches a reviewer:

```yaml
- name: Audit the ari setup
  run: ari doctor
```
