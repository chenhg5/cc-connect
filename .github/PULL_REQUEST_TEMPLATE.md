<!--
Thanks for contributing! Please fill in the sections below.
The reviewer will use the checklist at the bottom to gate merge.
-->

## Summary

<!-- 1-3 sentences explaining WHAT this PR does and WHY. -->

## Type of change

<!-- Mark all that apply: -->

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that would cause existing behavior to change)
- [ ] Documentation only
- [ ] Internal refactor / chore (no user-visible change)

## Testing

<!-- Explain how you verified the change. Be specific. -->

### Automated tests added in this PR

<!-- List the test functions you added or modified. -->

- `TestX_Y_Z` in `path/to/file_test.go` — what it asserts

### For bug fixes only — regression test

<!-- A bug fix PR MUST include a regression test that:
     (1) fails on the pre-fix code, and
     (2) passes on the fixed code.
     Name it so the bug is searchable later. -->

- Regression test name: `Test...`
- Manual verification this test catches the regression:
  - [ ] Reverted the fix locally; the regression test failed as expected.

### Critical User Journeys (CUJ) impact

<!-- See AGENTS.md → "Critical User Journeys (CUJ)" and the inventory in
     projects/cc-connect/agents/qa-cursor/release-gate/CUJ-INVENTORY.md.
     Mark which CUJ groups this PR touches: -->

- [ ] No CUJ touched (small refactor, doc change, etc.)
- [ ] A — basic conversation
- [ ] B — session lifecycle (`/new` `/switch` `/list` `/history` etc.)
- [ ] C — agent execution control (`/mode` `/cancel` `/stop` permissions)
- [ ] D — security & permissions (`allow_from` `admin_from` `banned_words` rate limits)
- [ ] E — scheduled tasks (`/cron` `/timer`)
- [ ] F — config switching (`/lang` `/provider` `/model` reload)
- [ ] G — error handling & robustness (LLM failure, ws reconnect, agent crash)
- [ ] H — multi-platform / multi-project isolation
- [ ] I — UI rendering correctness (cards, streaming, display modes)

If any CUJ group is touched, confirm:

- [ ] `go test ./core/ -run TestCUJ` passes locally.
- [ ] If the change alters an existing user-visible flow, the corresponding
      CUJ test was updated (or a new CUJ added) to cover the new behavior.

## Manual / user-visible behavior change

<!-- Describe what a user will see or do differently after this PR.
     If none, write "None". -->

## Checklist (reviewer will verify)

- [ ] `go build ./...` passes
- [ ] `go test ./...` passes (with `-race` if touching concurrency)
- [ ] AGENTS.md Pre-Commit Checklist items are satisfied
- [ ] No new hardcoded platform/agent names in `core/`
- [ ] i18n strings have all-language translations (if any new user-facing text)
- [ ] No secrets / credentials in source

## Related

<!-- Link to issue, RFC, design doc, prior PR, etc. -->

- Issue:
- Related PR:
