## Code review instructions

> ### ⛔ MANDATORY OUTPUT FORMAT — READ THIS FIRST ⛔
>
> **Every single review comment you post MUST begin with this exact prefix, with no exceptions:**
>
> ```
> [SEV: security|core|edge|cosmetic] [fix-now|defer-ok] <one-sentence summary>
> ```
>
> - This is a hard requirement, not a suggestion. A finding without this prefix is **invalid and must not be posted**.
> - Apply it to **every** inline comment on **every** review, including re-reviews.
> - Choose the severity from the actual impact (see "Finding severity" below): `security` and `core` are always `fix-now`; `edge`/`cosmetic` are usually `defer-ok`.
> - If you cannot classify a finding into one of the four severities, do not report it.
> - Before submitting the review, verify that **100% of your comments** carry the prefix. Do not submit an untagged comment.

When performing a pull request review, follow these instructions for selecting,
classifying, and reporting findings.

### Review objective

Review the proposed changes for defects that could make the pull request unsafe,
incorrect, insufficiently tested, or unnecessarily difficult to maintain.

Focus on behavior introduced or affected by this pull request. Inspect surrounding
code when necessary to understand the change, but do not report unrelated
pre-existing problems unless the pull request makes them materially worse.

Report only findings that are specific, reproducible, and actionable. Do not
report speculative concerns without identifying a concrete failure mode.

### Review priorities

Review in this order and allocate attention accordingly. The examples are
representative, not exhaustive; report other findings that satisfy the category
definitions.

1. **Security** — exploitable vulnerabilities or sensitive-data exposure,
   including but not limited to SQL injection, embedded secrets (passwords, keys,
   tokens, or a hash of a secret committed in source), unsafe deserialization,
   and sensitive data exposed through logs, errors, or query output.
2. **Correctness** — behavior that produces an incorrect result or state,
   including but not limited to logic errors, broken primary flows, incorrect
   data mutations, wrong migration up/down behavior, race conditions, unhandled
   asynchronous failures, and resource/connection leaks.
3. **Edge cases** — incorrect behavior under bounded or unusual conditions,
   including but not limited to empty, null, zero, maximum, or malformed inputs;
   off-by-one errors; timezone and encoding issues; pagination boundaries;
   retries; and concurrent access.
4. **Tests** — inadequate verification of changed behavior, including but not
   limited to missing coverage, tests that would pass if the implementation were
   broken, assertions that do not verify the intended outcome, and important
   failure paths that are not exercised.
5. **Maintainability** — code qualities that create a concrete risk of future
   defects, including but not limited to dead code, unnecessary duplication,
   misleading names, incorrect documentation, and avoidable complexity.
6. **Style** — review last and only when the code violates this repository's
   documented conventions. Do not report personal preferences or formatting
   that an automated formatter should handle.

### Finding severity

Assign exactly one of the following four severity labels to every finding. These
four labels are exhaustive, but the examples listed under them are not. Classify
unlisted problems according to their actual impact.

- `[SEV: security]` — an exploitable vulnerability, secret exposure, or
  sensitive-data disclosure. Always `fix-now`.
- `[SEV: core]` — breaks, corrupts, or materially compromises a primary feature,
  data path, or expected workflow. Always `fix-now`.
- `[SEV: edge]` — produces incorrect behavior in a bounded, non-primary case.
  Use `fix-now` when the case can cause meaningful harm; otherwise use
  `defer-ok`.
- `[SEV: cosmetic]` — affects naming, documentation, readability, or documented
  style without changing runtime behavior. Normally `defer-ok`.

Use the impact of the actual failure—not the size of the proposed fix—to assign
severity.

### Reporting findings

- Report one distinct finding per comment.
- Anchor the comment to the smallest relevant changed line or range.
- Use this exact opening format:

  `[SEV: <security|core|edge|cosmetic>] [<fix-now|defer-ok>] <one-sentence summary>`

- After the opening line, explain:

  1. the conditions that trigger the problem;
  2. the incorrect or unsafe result; and
  3. why the pull request causes or exposes that result.

- Propose a concrete correction. Include a code suggestion when the change is
  local and unambiguous.
- Do not report a theoretical concern unless you can describe a plausible
  execution path or input that triggers it.
- Do not duplicate a finding across lines or files. Report it once and mention
  other known occurrences in the same comment.
- Do not request defensive checks on every internal call unless required by a
  documented project rule or a demonstrated failure mode.
- Do not report issues already enforced reliably by the repository's formatter,
  linter, compiler, or type checker unless the pull request demonstrates that
  the enforcement is absent or bypassed.

Example of a correctly formatted finding (note the bare prefix, with no
surrounding backticks):

> [SEV: core] [fix-now] The stage moves the file at its original client path, so a client's source artwork is relocated instead of a staged copy.
>
> `upload-to-flickr` moves `file.pathDisplay` into the done folder. When the manifest record points at the original Dropbox path rather than a CANDIDATES staging copy, that move relocates the client's original file out of its source folder.
> This pull request changes the manifest to carry original paths.
>
> Suggested fix: stage a working copy and point the record at it, so the move never touches the original.

### Review summary

Conclude with a short summary containing:

- the number of findings at each severity;
- whether any `fix-now` findings remain;
- the highest-risk area reviewed;
- any important behavior that could not be verified from the pull request; and
- an explicit approval recommendation on its own final line.

State the recommendation as exactly one of the following:

- `RECOMMENDATION: Request changes` — one or more `fix-now` findings remain.
- `RECOMMENDATION: Approve with suggestions` — only `defer-ok` findings remain.
- `RECOMMENDATION: Approve` — no actionable findings remain.

If there are no actionable findings, state explicitly:

`No actionable security, correctness, edge-case, testing, maintainability, or documented-style issues found.`

Do not invent findings merely to populate the review.

### Project rules that constrain the review

Treat the following repository rules as authoritative. Do not flag code that
follows them. When a finding depends on a project rule, cite the relevant rule
or file in the comment.

- **Rule sources:** `README.md`, `docs/ARCHITECTURE.md`, `CHANGELOG.md`, and the
  `docs/adr/` decision records relevant to the changed code.
- **What this repo is:** `@atomiclotus/gh-core`, a shared library published to npm
  and consumed by other repositories (for example gh-flickr and gh-pngme). It has
  no application entry point, CDK stack, or deployment of its own — its public
  surface is the exports from `index.js` and the per-directory `index.js` files
  under `src/` (`src/services/`, `src/aws/`, `src/entities/`, `src/utils/`). It
  provides service classes (`DropboxService`, `DropboxAuthService`,
  `FlickrService`, `ClientCodeService`), AWS wrappers (`S3Service`, `SnsService`,
  `SqsService`), entities (`DropboxFile`, `ClientCodes`, `FileTypes`,
  `Resolutions`), and utilities that wrap the Dropbox, Flickr, and AWS SDKs.
  Dependencies are injected through constructor options (auth providers, HTTP
  request functions, SDK clients) so behavior is testable at real boundaries;
  do not request redundant defensive validation on trusted internal calls or
  documented library guarantees.
- **Destructive Dropbox operations:** consumers rely on this library to perform
  Dropbox file and folder operations, including moves and deletes. Any change
  that could move, delete, or overwrite a client's original source file (rather
  than a staged working copy) is `[SEV: core] [fix-now]`. When the library
  processes a batch, a single-item failure must be skipped and logged, never
  fail the whole batch, unless the caller's contract explicitly says otherwise.
- **Secrets and tokens:** configuration comes from the consumer's environment
  (read via `process.env`) and, increasingly, AWS Secrets Manager; never hardcode
  a password, key, token, or a hash of a secret in source — any such literal is
  `[SEV: security] [fix-now]`. `DropboxAuthService` handles OAuth refresh and
  access tokens; a change that logs, prints, or otherwise exposes a token or
  refresh token is `[SEV: security] [fix-now]`.
- **Public API and versioning:** because this is a shared package, its exports
  are a contract. Flag any change that removes, renames, or alters the signature
  of an exported symbol (in `index.js` or a `src/**/index.js`) that is not
  accompanied by a corresponding SemVer version bump in `package.json` and a
  `CHANGELOG.md` entry — a breaking public-API change without a major bump is
  `[SEV: core] [fix-now]`.
- **Coding conventions:** `camelCase` for variables/functions; `PascalCase` for
  classes, enums, and static objects; `UPPER_SNAKE_CASE` for global constants
  (prefix env-derived constants with `k`); `lower_snake_case` only for database
  column names. Align object properties on the colon. Put `else`/`else if` on its
  own line; prefer an early `return`/`throw` over a redundant `else`. Use
  ternaries only to choose between two values, never actions, and no more than
  two per expression. Document functions with JSDoc.
- **Testing conventions:** Jest. Tests live under `src/test/`. Every test opens
  with a comment stating its scenario. Seed realistic domain data — no
  `foo`/`bar`/arbitrary IDs. Freeze time to a fixed ISO timestamp; no future
  dates. A test must fail if the implementation it covers is removed.
- **Generated or vendored code:** Do not give source-level review to lockfiles
  (`package-lock.json`) or Jest `__snapshots__`.
