---
name: openspec-verify-change
description: Exploratory end-to-end QA of an OpenSpec change against the real running system — browser-driven user journeys derived from the spec's intent, not from task checkboxes or unit tests. Use after implementation and before archiving, when the user wants to verify/QA/test a change end-to-end, do manual-QA-style checking, or confirm the change actually works for a user. Not for writing unit tests (use tdd for that).
license: MIT
compatibility: Requires openspec CLI.
metadata:
  author: gio
  version: "1.1"
---

Verify an implemented OpenSpec change end-to-end: bring up the real system,
exercise it the way a user would (browser first), and judge it against the
change's **intent** — not the task list, not green test suites.

**Why this exists:** unit and integration tests prove the pieces behave as
their authors described them. They say nothing about whether the assembled
product delivers what the spec promised. This skill is the manual-QA pass:
journey-driven, exploratory, evidence-based. Its output is *observed
behavior*, or it is nothing.

**Input**: Optionally specify a change name. If omitted, check if it can be
inferred from conversation context. If vague or ambiguous you MUST prompt for
available changes. Verification is meaningful once implementation is
(mostly) done — if tasks remain open, say so and confirm before proceeding.

**Steps**

1. **Select the change**

   If a name is provided, use it. Otherwise infer from context, auto-select
   if only one active change exists (`openspec list --json`), or use the
   **AskUserQuestion tool**. Always announce: "Verifying change: <name>".

2. **Read the spec as a QA lead, not as the implementer**

   Run `openspec status --change "<name>" --json` and read every file
   listed under `artifactPaths.*.existingOutputPaths` — status lists only
   files that actually exist. Read them **in this order, for this purpose**:

   1. `proposal.md` → the promise. Write one sentence: *"After this change,
      a user can ____."* Note who the users are and what they could not do
      before.
   2. `design.md` → decisions that are user-observable (mode gating, TTLs,
      failure degradation, auth behavior).
   3. Delta specs → every `#### Scenario:` a user could observe through the
      product surface (UI, public URLs, documented API). These are the
      contract; each observable one becomes a checkpoint.
   4. `tasks.md` **last**, and only for coverage checking: a task that no
      journey touches means either your journey set is incomplete or the
      task isn't user-visible. Never use the task list as the test plan —
      that would just re-verify the implementation's shape.

   Do not read implementation code (`internal/`) to decide what to test.
   Reading code is allowed later, and only to root-cause a failure (step 6).

   Produce a **verification charter** you will keep visible in the report:

   - **Promise** — the one sentence.
   - **Journeys** (3–7): named user stories with a start, actions, and an
     observable outcome. Quality bar: a journey goes through the real
     product surface (browser UI, public `/p/{slug}/` URLs, documented
     `/api/*` endpoints) and ends in something the user cares about — a
     page renders, a state change is visible, an error is understandable.
     "POST /api/pages returns 201" is not a journey; "uploader drops a zip,
     follows the returned URL, and sees their page render with styles and
     images" is.
   - **Exploratory charters** — what to poke at; filled in step 5.

   Coverage rule: at least one golden-path journey per capability the delta
   specs touch, and at least one failure-path journey per *observable*
   failure scenario in the deltas. Internal-only scenarios (locks,
   migrations, counters) stay covered by integration tests — don't
   browser-test those.

3. **Bring up the real system**

   - This repo: `make up` (Docker: MinIO :9000, console :9001, Postgres),
     then start `make run` as a background task (Bash run_in_background —
     keep its task id for teardown). Server on :8080, auth token
     `devtoken`; current values per AGENTS.md. Poll `/healthz` until
     ready. `make seed` gives a known-good baseline page at
     `/p/sample-1/` — useful to confirm serving works before testing the
     change itself.
   - If the change touches `SERVER_MODE` behavior, one `all`-mode instance
     cannot verify the mode contract: boot `serve` and `admin` instances
     on separate `ADDR`s (env matrix in AGENTS.md) and check the
     cross-mode 404s — serve must 404 `/` and `/api/*`, admin must 404
     `/p/*` and `/a/*`, both must answer `/healthz`.
   - Use fresh inputs with unique identifiers (e.g. `qa-<change>-<n>`) so
     re-runs don't collide with existing slugs.
   - If Docker or the server can't come up: STOP and report the environment
     problem. Never substitute unit-test results or the `mem` driver for a
     live run — that is exactly the failure mode this skill exists to
     prevent.
   - Do not reconfigure the app to make testing easier (no raised caps, no
     disabled auth) — you would be verifying a different product. If a cap
     blocks a legitimate journey, that is a finding.

4. **Run the journeys**

   - Load the `browser-use:control-browser` skill (or
     `browser-use:web-gui-tester` for pure black-box GUI passes) before any
     browser interaction. Use curl for `/api/*` journeys and HTTP-level
     observations (status codes, headers, redirects).
   - Follow the user's path, not the code's: click the buttons the UI
     actually offers; type into the real form. Drop to curl only when the
     spec's surface is the API itself.
   - Capture evidence for every step that matters: a screenshot (browser
     actions, state changes) or an HTTP transcript (API calls). Save to
     `/tmp/openspec-verify/<change>/` and reference the paths in the report.
     /tmp does not survive a reboot and the report does: if the run ends
     with findings, copy only the decisive items (at most five, findings
     only) into `openspec/changes/<name>/evidence/` so the fix cycle has
     them.
   - Where timing is part of the spec (e.g. a cache TTL on parked pages),
     observe it: act, check immediately, check again after the window.
   - Record per journey: steps taken, expected vs observed, evidence paths,
     and a verdict — `pass` (matched intent), `fail` (contradicted a
     scenario or the promise), `degraded` (completed, but with quality
     erosion short of contradicting the spec), `blocked` (could not
     execute — usually environment).

5. **Exploratory pass**

   Time-box this (roughly the same effort as step 4, or until charters are
   exhausted). Perturb around the golden paths — anything the deltas promise
   must survive perturbation:

   - **Inputs**: empty file, wrong extension, oversize upload, huge
     decompressed zip, unicode/space/path-like identifiers, duplicate
     identifier.
   - **Sequence**: double-submit, refresh mid-upload, back button after
     success, repeating a journey twice with the same input.
   - **State**: direct access to assets of a parked page; the cache window
     after park/unpark; slugs that don't exist.
   - **Boundaries**: missing/wrong bearer token; HTTP methods the API
     doesn't document; deeply nested or unusual paths.
   - **Recovery** (only if cheap): kill the server mid-journey, restart,
     check state consistency.

   **Follow the smell:** any unexpected behavior — an odd message, a slow
   response, a strange URL — gets probed three more ways before you move
   on. Exploratory findings are often the real reason for this skill; a
   perturbation that reveals nothing is also worth one line in the report.

6. **Classify what failed**

   For each `fail`/`degraded`: reproduce it once cleanly, then — and only
   now — read the implementation, just deep enough to classify:

   - **Code bug** — behavior contradicts a delta scenario or the design.
   - **Spec gap** — behavior is reasonable but the spec didn't anticipate
     it; the artifacts need updating, not the code.
   - **Intent gap** — every scenario passes, yet the promise from step 2
     still isn't delivered for the user. The most important category this
     skill exists to catch. Example: all endpoints return 200, but the new
     management UI lists pages the user can't actually open from it.
   - **Environment** — infra or flake; retry once, then note it. Not a
     product failure.

7. **Write `verification.md` in the change directory, then report**

   Write the report to `openspec/changes/<name>/verification.md` so it
   travels with the change into archive. Evidence stays in /tmp except the
   decisive copies step 4 makes when there are findings. The overall
   verdict follows the worst finding: an intent gap → `intent not met`;
   any `fail` or open spec gap → `delivered with findings`; otherwise
   `delivered`, with degradations noted. `blocked` journeys make the run
   inconclusive — report that instead of a verdict. Use this shape:

   ```
   ## Verification: <change-name> (<date>)

   **Promise:** after this change, a user can …
   **Verdict:** delivered / delivered with findings / intent not met

   | Journey | Exercised intent | Verdict | Evidence |
   |---|---|---|---|
   | J1 upload→view | page-upload, ingest-pipeline | pass | /tmp/…/j1-*.png |

   ### Findings
   - [major] <what happens> — expected vs observed — <classification> — evidence

   ### Exploratory notes
   - <what was poked, what held, what wobbled>

   ### Coverage
   - Delta scenarios not observable in the product surface (left to
     integration tests): <list>
   ```

   Then stop and hand the decision to the user. Findings → suggest fixes
   (openspec-apply-change) followed by a scoped re-verify: only the failed
   journeys plus one golden-path smoke, updating `verification.md` in
   place rather than writing a second report. Clean → suggest
   openspec-archive-change. Never archive from this skill, and never fix
   code inside a verification run — QA that fixes its own bugs destroys its
   own evidence.

**Guardrails**

- Never declare success from `make test` or integration results alone —
  they are necessary, not sufficient.
- Journeys come from the artifacts, not from the code (until diagnosis).
- Every claim cites evidence: a screenshot path or an HTTP transcript. No
  "should work".
- Don't weaken configuration to make tests pass; report blockers instead.
- This skill writes exactly two places: the change's `verification.md`, and
  — only when there are findings — at most five decisive files under the
  change's `evidence/`. Leave the codebase untouched.
- Leave the environment as you found it: stop the server you started, note
  leftover slugs/objects you created, say what is still running.
