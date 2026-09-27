# Contributing

This project is a fork of [ingestr](https://github.com/bruin-data/ingestr).
Two rules apply to every contribution, on top of normal code review: no AI
attribution in tracked history, and no sensitive or local-only files.

## No AI attribution

No commit, tag, pull request, release, or tracked file may carry AI
attribution. This includes:

- `Co-Authored-By` trailers naming Claude, Anthropic, Copilot, Cursor,
  ChatGPT, or similar AI tools,
- "Generated with ..." footers, and
- robot emoji markers.

This applies whether or not you used an AI tool to help write the change —
the rule is about what ends up in tracked history and public-facing text, not
about how the change was produced. `fabric/scripts/check-attribution.sh`
enforces this via the commit-msg hook and in CI, over commit messages and
over the pull request title and body.

## No sensitive or local-only files

Never commit or push:

- Build briefs (`briefs/`, `*BUILD_BRIEF*`, `*.brief.md`),
- Environment and secret material (`.env`, `.env.*`, `*.token`, `*.pem`,
  `*.key`, `*.pfx`, `secrets/`, `token_output*`),
- Runtime state and outputs (`fabric/state/`, `fabric/verification/results/`,
  `*.duckdb`, `*.duckdb.wal`, `*.parquet` outside `pkg/**/testdata/`), or
- Local tool configuration (`CLAUDE.local.md`, `.claude/settings.local.json`,
  or any developer-agent files such as `CLAUDE.md`, `AGENTS.md`, `.claude/`,
  `.agents/`, `skills/`).

`fabric/scripts/check-sensitive-files.sh` enforces this via the pre-commit
hook and in CI. If it rejects a file you believe should be tracked, that's a
sign the pattern needs narrowing in that script, not a reason to bypass the
hook.

## Bullhorn data

Never post real Bullhorn data, credentials, tenant identifiers, or
screenshots containing records in an issue, PR, commit, or comment. Test
fixtures are built only from the public
[Bullhorn REST API docs](https://github.com/bullhorn/rest-api-docs) examples,
never from a live account.

## Before opening a pull request

```bash
make format
make lint
make test
make licenses-audit
```

All four must pass. See `.github/pull_request_template.md` for the full
checklist.
