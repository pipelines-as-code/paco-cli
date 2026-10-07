# ReviewBench: local settings comparison

## Conclusion

Paco's next improvement should be to check suspected problems more carefully
and finish reviews reliably. Turning up the reasoning effort made reviews
roughly twice as slow and caused seven failures instead of one. We should
keep the current low-effort setting while improving how it works.

I would prioritize these changes:

1. Make sure it finishes. Paco sometimes spends its entire allowance
   investigating and never delivers a review. It should stop exploring early
   enough to return the problems it has actually confirmed. The experimental
   double-checking mode needs this fixed before we can judge its usefulness.
2. Require evidence before raising a warning. In one review, Paco warned
   about a missing safety check that already existed. Its suggested fix would
   have broken the intended behavior. Before making that kind of claim, it
   should read the relevant function and identify an example that would
   genuinely fail.
3. Follow what the code actually does. Paco caught a test that called a
   pretend replacement instead of the real function it was supposed to test.
   Checking which functions get called, and what tests really exercise, is a
   useful direction for finding problems beyond the changed lines.
4. Be more cautious with ready-to-apply fixes. A correct warning does not
   guarantee a correct fix. Paco should offer replacement code only when it
   fits the selected lines and preserves the surrounding behavior. Otherwise,
   it should explain what needs changing.

After repairing the benchmark's automatic grading, 49 of the 50 reviews have
a grade. The grades also favor low effort. On the 24 PRs graded at both
settings, low effort raised 46 real problems out of 55 comments; high effort
raised 29 out of 33. Low effort is a little noisier (84% of its comments were
right against 88%), but it found about 60% more real problems, and
it led on the combined score in every way we sliced the data. This is one
run per setting graded by a Gemini judge, so treat it as strong support for
keeping low effort rather than a final measurement.

Future comparisons should include changes with no bugs, so we can measure
unnecessary warnings. The four changes above are worth testing; the
benchmark has not yet proved that changing the prompts will deliver them.

All four are now implemented in paco but have not been measured. See
[Changes made after this report](#changes-made-after-this-report).

## Scope and settings

This was an ad hoc local comparison, with no CI corpus runs, image publication,
GitHub review posting, or leaderboard submission. It covered all 25 PRs in
ReviewBench's public test manifest, once per setting, with two reviewer
containers running concurrently. Failed reviews were not retried.

| Setting | Value |
| --- | --- |
| Reviewer | Vertex AI `claude-opus-4-6@default` |
| Compared strategies | `single/low`, `single/high` |
| Repository exploration / web search | Both enabled |
| API-enforced structured output | Disabled; local output validation retained |
| Single-pass allowance | 8 model turns, 24 repository calls, 3 web searches |
| Adapter timeout | 780 seconds |
| Diff size limit | 200,000 bytes |
| Judge | Vertex AI `gemini-2.5-pro`, concurrency 2 |
| Paid judging allowance | One smoke test, then one pass per configuration |

The corpus contains 352 golden true positives across these 25 PRs. All selected
PRs have golden findings; this experiment cannot measure the false-positive
rate on clean PRs. Three diffs exceeded paco's size limit, leaving 22 eligible
PRs and 316 golden true positives.

The adapter supplies pinned head and reconstructed before snapshots. It omits
production base-branch review rules, toolchain declarations and prior review
feedback. Golden findings were available only to the judge.

## Operational results

| Measure | Single / low | Single / high |
| --- | ---: | ---: |
| Completed reviews | 21/25 | 15/25 |
| Failed reviews | 1/25 | 7/25 |
| Skipped oversized diffs | 3/25 | 3/25 |
| Completion among eligible PRs | 21/22 (95.5%) | 15/22 (68.2%) |
| Findings emitted | 58 | 36 |
| Median elapsed, eligible attempts | 25.9 s | 55.2 s |
| Median elapsed, successful reviews | 25.3 s | 64.6 s |
| Slowest successful review | 81.4 s | 91.0 s |
| Model requests / turns | 59 | 154 |
| Repository calls | 45 | 232 |
| Web searches | 2 | 11 |
| Eligible attempts with no repository calls | 12/22 | 0/22 |
| Input tokens | 875,942 | 3,796,409 |
| Output tokens | 22,732 | 52,539 |
| Total reviewer tokens | 898,674 | 3,848,948 |

Elapsed times cover each container invocation against an already prepared
checkout. They exclude image building, corpus fetching and judging. Usage
includes failed reviews but excludes the earlier diagnostic runs and judge
calls. Token counts are provider-reported, not a currency estimate.

Every review failure hit the model-turn limit, rather than the wall-clock
timeout. Raising `RB_CONFIG_TIMEOUT` would not address these failures. High
effort increased exploration without increasing the eight-turn allowance.
Low effort's zero-tool runs do not mean it had no context: the diff was
already in its prompt.

### Per-PR coverage

Numbers in the last two columns are emitted findings, not true positives.
`failed` means turn exhaustion and no successful findings output; `skipped`
means the diff exceeded the size limit. A completed review with `0` findings
is distinct from either outcome.

| PR | Golden TPs | Low findings | High findings |
| --- | ---: | ---: | ---: |
| PierreJanineh/TechDebtMCP#135 | 9 | 0 | 1 |
| simnaut/emdash#2 | 6 | skipped | skipped |
| konturio/disaster-ninja-fe#1230 | 13 | 2 | failed |
| cmik/apilix#12 | 36 | 7 | failed |
| AA-Factory/aafactory-prototype#17 | 9 | 0 | 2 |
| DisciplinedSoftware/Codebase-Conversion#1 | 31 | 8 | 5 |
| brianc/node-postgres#3650 | 11 | 0 | failed |
| PaulStSmith/figlet-comment-generator#32 | 9 | 6 | 4 |
| brexhq/CrabTrap#15 | 16 | 2 | 0 |
| k1LoW/gh-copilot-review#9 | 7 | 2 | 1 |
| scotthamilton77/agents-config#1 | 25 | 0 | 1 |
| ardevd/jadx-collaboration#3 | 9 | 3 | 3 |
| AY2526S2-CS2103T-W14-3/tp#152 | 14 | 3 | failed |
| vanyastaff/nebula#224 | 9 | skipped | skipped |
| NCATSTranslator/translator-ingests#336 | 17 | 9 | 9 |
| mheuss/chronicle#14 | 10 | 3 | failed |
| superhighfives/pika#200 | 19 | 3 | 6 |
| gagneurlab/PROTRIDER#10 | 21 | skipped | skipped |
| allan-mobley-jr/gimmes#468 | 6 | 0 | 0 |
| superyyrrzz/ForgeMap#106 | 15 | 0 | 0 |
| renedierking/LANdalf#88 | 6 | 0 | 0 |
| scottlz0310/Mcp-Docker#29 | 22 | 4 | 2 |
| wirechat/wirechat#183 | 12 | 3 | failed |
| sdsykes/fastimage#165 | 14 | 3 | 2 |
| Marvell-Consulting/statswales-frontend#626 | 6 | failed | failed |

## Quality scoring

ReviewBench's grounded precision divides matched true-positive candidates by
matched candidates, excluding unmatched predictions. Augmented precision
includes classifications of unmatched findings and is the more useful measure
of overall comment quality. Grounded recall measures coverage of golden true
positives. Augmented recall adds novel true positives to both numerator and
denominator. Empty predictions have undefined precision. Any F1 reported here
is derived from micro precision and recall.

Scoring inputs included all 25 PRs. Failed reviewer runs received empty
findings in a separate scoring-only directory, with an explicit placeholder
ledger; original failure artifacts were preserved. This keeps failures in the
recall denominator when judging succeeds. Judge failures remain unknown
rather than becoming empty reviews or false positives.

The first judge passes did not finish. A smoke test had matched one finding
correctly, but it never asked the judge to classify an unmatched finding,
and that is where the corpus passes broke: Gemini repeatedly returned
replies the harness could not parse (`no JSON found`). Eight low-effort PRs
and one high-effort PR were left unjudged, so the strict evaluator refused
an aggregate for both settings. These were judge failures and say nothing
about paco's precision. After the [judge repair](#judge-repair), a retry
re-judged only those PRs.

### Scores after the grading repair

Low effort now has judgments for 24/25 PRs and high effort for all 25; high
effort also passed strict aggregation. The remaining gap is
ardevd/jadx-collaboration#3 at low effort (3 findings). Gemini ended that
session with a provider-side stop three times, so it stays unjudged rather
than counted as clean or wrong.
than counted as clean or wrong.

| Measure on the same 24 PRs | Low | High |
| --- | ---: | ---: |
| Candidate findings | 55 | 33 |
| Matched TP / matched candidates | 30/35 | 21/24 |
| Novel TP / unmatched candidates | 16/20 | 8/9 |
| Golden TPs covered | 34/343 | 21/343 |
| Grounded precision | 85.71% | 87.50% |
| Grounded recall | 9.91% | 6.12% |
| Augmented precision | 83.64% | 87.88% |
| Augmented recall | 13.93% | 8.26% |
| Derived grounded F1 | 17.77% | 11.44% |
| Derived augmented F1 | 23.88% | 15.10% |

High effort over all 25 PRs, from its strict `scores.json`, had 88.00%
grounded precision, 6.25% grounded recall, 88.89% augmented precision and
8.84% augmented recall.

| Paired subset | PRs / golden TPs | Low augmented P / R / F1 | High augmented P / R / F1 |
| --- | ---: | ---: | ---: |
| Eligible (diff within limit) | 21 / 307 | 83.64% / 15.48% / 26.12% | 87.88% / 9.21% / 16.67% |
| Reviewer succeeded at both settings | 14 / 205 | 83.78% / 16.04% / 26.92% | 87.88% / 13.62% / 23.58% |
| Excluding the diagnostic PR | 23 / 334 | 83.64% / 14.29% / 24.40% | 87.10% / 7.89% / 14.48% |

Low effort leads on recall and F1 in every view. High effort is about four
points more precise. The gap narrows when only PRs where both reviews
succeeded are compared, because most of high effort's recall loss comes
from its seven turn-limit failures, but low effort still leads there.

By severity on the 24 paired PRs, low effort covered 1/32 high, 20/133
medium and 13/178 low-severity golden findings; high effort covered 3/32,
15/133 and 3/178. Both settings also raised real high-severity problems
missing from the golden set: eight at low effort and five at high effort,
as judged by the classifier. Three high-severity matches are still too few
to claim that high effort finds severe bugs better.

These scores mix two harness versions. PRs that completed in the original
passes keep their original judgments; the retried PRs were judged again
from scratch, including new matcher calls, by the repaired harness. Both
versions use the same model, prompts, labels and scorer. The checkpoint
fingerprints matched, but they do not cover parser code.

### Judge repair

The `no JSON found` errors came from Gemini sometimes ending a turn normally
with a reasoning block and no answer text. The local harness patch used for
the original passes had hidden this by removing two upstream behaviors:
automatic provider retries, and a single re-ask ("respond with only the JSON
object") when a classification was unusable.

The repair lives in the local ReviewBench checkout; paco's reviewer is
untouched. The judge now reads all answer text blocks in order, where the
classifier had reversed them and the matcher kept only the first. It rejects
incomplete replies instead of falling back to earlier commentary, checks
labels and field types, and reports parsing failures without logging answer
text or provider error contents. A second change restores upstream's single
re-ask, with upstream's wording, for empty, missing, malformed or invalid
answers, and lets the setup turn, whose text the harness never reads, finish
with no text. Provider errors and truncated replies still fail without
another request. Prompts, scoring formulas, allowed labels and the
`google-vertex/gemini-2.5-pro` model are unchanged, and automatic provider
retries stay off. Offline regression tests cover each case, using the reply
shapes seen in the captures.

Before any retry, one live diagnostic ran the classifier on
AA-Factory/aafactory-prototype#17 with one true finding (the test calls the
mocked helper instead of the production wrapper) and one false one (the test
never clears shared chat history, when it does). Gemini labelled both
correctly. Its replies were also readable by the old parser, so this showed
the repaired classifier could finish a real session without explaining the
original failures.

The retries ran in copies of the original output directories at concurrency
1, with temporary capture of final answer text and reply metadata.

| Retry pass | Low | High |
| --- | --- | --- |
| 1, response repair only | 4 of 8 scored; 4 `empty-answer` | 0 of 1; `empty-answer` on setup |
| 2, with re-ask and setup fix | 3 of 4 scored; 1 provider error | 1 of 1 scored |
| 3, ardevd only | Provider error after one re-ask | n/a |

ardevd/jadx-collaboration#3 kept ending in a provider-side stop. The SDK
reports these as an unknown error and drops Gemini's finish reason, so the
harness cannot show the cause, and I stopped retrying after the third pass.
Of 38 captured replies across the passes, 25 had answer text, 11 had only a
reasoning block, and 2 were provider errors. The captures were deleted once
a text-free tally of reply shapes was saved.

The session keeps `judge-repair.patch` with the new helpers and tests, plus
`judge-repair-provenance.json` and the diagnostic result. That patch also
contains the earlier isolation changes and applies to the original
ReviewBench commit recorded below. Its SHA-256 is
`7b22f0a859c0d57144a4704fbcda890a65e5db52016137591ebabe386bed9551`.

Judge usage for the original passes, separate from reviewer usage:

| Pass / phase | Input tokens | Output tokens | Classifier cache-read tokens |
| --- | ---: | ---: | ---: |
| Low / matcher | 91,629 | 53,706 | n/a |
| Low / classifier | 498,750 | 63,438 | 261,213 |
| High / matcher | 51,333 | 32,687 | n/a |
| High / classifier | 339,790 | 41,115 | 164,037 |

These counters include failed judge attempts and exclude the smoke test.
Matcher cache usage was not instrumented, so the table is not a complete
billing estimate. The retries' classifier sessions reported 1,261,919 input
and cache-read tokens, 158,514 output tokens and an SDK estimate of USD
1.73; matcher calls were not priced.

## Verified-mode diagnosis

Verified/high did not pass the usability gate and was excluded from the
25-PR matrix. Three additional diagnostic runs on
AA-Factory/aafactory-prototype#17 produced:

1. A discovery JSON object inside a terminal `json` fence, preceded by prose.
   Strict JSON parsing rejected the envelope before evidence validation.
2. Discovery turn exhaustion after the envelope fix.
3. Another discovery turn exhaustion with the same limits.

The parser now accepts that narrowly defined envelope while rejecting
multiple objects, multiple fences and trailing commentary. Schema checks,
source-evidence checks and secret scanning remain in place. Temporary
scrubbed response captures were deleted after diagnosis.

Discovery has four turns within the shared budget. These failures occurred
before verification; they do not show that the verifier rejected valid
findings. Increasing the timeout or claiming a verification precision gain
would not follow from this evidence.

## Changes made after this report

The four recommendations are in the code. Only offline tests cover them:
fake-model tests check the request shape and the guard logic. No live
review or benchmark run has measured their effect. The scores above
describe the earlier code and prompt digest.

The model loop keeps the last allowed turn for the answer. That request
sends `tool_choice: none`, drops web search and adds an instruction to
return only confirmed findings. When the model asks for more repository
calls than remain, those calls get an error result and the next turn is
the final one. This applies to single-pass and every verified phase.
Verified discovery still has a four-turn limit, so it now explores for at
most three turns before it must answer; raising that limit is still open.
[Anthropic's documentation](https://platform.claude.com/docs/en/build-with-claude/thinking-tool-workflows)
allows `tool_choice: none` with both manual and adaptive thinking. We have
not sent it through Vertex yet, so the first live run should confirm it.

The review prompt now has a rule for tool-backed reviews. Before reporting
a missing guard or error conversion, the reviewer must read the called code
and name a failing input, and drop the finding if that code already handles
the case. It must also confirm that a changed test calls the real code
rather than a mock, and check the callers of a changed signature. The
verified discovery prompt carries the same rules.

Single-pass output now goes through a suggestion check. Paco removes the
suggestion block, keeping the explanation, when the fence is malformed or
repeated, when the replacement equals the current line, or when a
multi-line replacement repeats the lines next to the anchor and would
duplicate code. A comment that held nothing but the suggestion is dropped.
Other multi-line suggestions pass, because replacing one line with several
can be correct. The prompt also asks the reviewer to
apply the replacement in its head and leave it out when the fix needs a
broader edit.

To measure these changes, run the same matrix first on a development split
drawn from the other 194 corpus PRs, then once on the 25 test PRs. Compare
the turn-limit failure count, recall and the false positives checked
against source.

## Source checks and prompt advice

These sections record the advice as written from the benchmark evidence.
The section above describes what paco now does about it.

### Verify the alleged missing protection

Low effort warned that `PlannerListPanel.showNote(null)` might throw in
AY2526S2-CS2103T-W14-3/tp#152. At the pinned head,
[`showNote`](https://github.com/AY2526S2-CS2103T-W14-3/tp/blob/e0a9ce03c8c1cc839eaf409e2677b9abf2017b1f/src/main/java/seedu/address/ui/PlannerListPanel.java#L64-L73)
clears the note container and then explicitly returns on null or blank input. The proposed
caller-side null guard would prevent that clearing behavior.

Another comment speculated that `VisitDate.of` might leak parsing exceptions.
The checked date-parsing paths convert `DateTimeParseException` to
`IllegalValueException`. The comment supplied no concrete escaping input.
These examples support a specific addition to the existing anti-speculation
rule:

> Before reporting a missing guard or exception conversion, inspect the
> called implementation and relevant caller. Name a concrete failing input.
> If the implementation already handles it, discard the finding.

This does not require tools for every finding. It requires evidence when the
claim depends on code outside the supplied diff.

### Finish exploration within the budget

High effort used tools on every eligible PR but failed seven times. The
system prompt states the total budget, without a turn-by-turn finishing rule.
A useful experiment would batch related repository reads and require a final
answer before the last available turn:

> Batch independent reads for the current hypothesis. Reserve the final
> model turn for the required JSON. If evidence remains incomplete, omit
> that hypothesis rather than requesting another tool call.

First verify how well the model can track remaining turns. A runtime
remaining-budget signal or a reserved finalization turn may be more reliable
than prose alone. Neither change was tested here. For verified mode, address
discovery's four-turn limit before measuring verification quality.

### Check what tests actually exercise

On AA-Factory/aafactory-prototype#17, high effort found that a test called the
mocked API helper instead of the intended LLM wrapper; low effort emitted
nothing. The helper's argument signature also contradicted the assertion.
For changed tests, ask the reviewer to trace the called symbol and patch
target before judging coverage. Use other PRs to test this advice: this PR
was already used for diagnosis and cannot serve as held-out evidence.

### Keep anchors and suggestions applicable

An offline diff audit found 8/58 low-effort comments and 1/36 high-effort
comments anchored outside added lines. The adapter exports model comments
directly; production `paco post` filters such inline anchors. These benchmark
counts therefore do not equal the number of comments production would post.
Keep this distinction in future comparisons rather than silently filtering
the frozen candidates after scoring.

Eight low-effort and seven high-effort comments included multiline suggestion
blocks attached to single-line anchors. A multiline replacement can be valid;
the count alone does not prove a broken patch. The null-guard example above
does demonstrate a behavior-changing bad suggestion.

The current prompt already says a suggestion replaces only its targeted
line. Test a stronger preflight: mentally apply the replacement to exactly
that line, check the surrounding syntax and behavior, and omit the suggestion
if it needs a broader edit. Deterministic anchor checks should accompany any
prompt experiment.

## Limits on the recommendation

This is one run per setting, on a public positive-only corpus, using a Gemini
judge rather than the benchmark's published judge. Web search was enabled,
so public benchmark contamination cannot be excluded even though golden
files were never mounted into reviewer containers. Model aliases also do not
freeze provider weights.

Do not select high effort from the small diagnostic PR, select low effort
from raw finding counts, or compare quality averages over different
success-only subsets. A follow-up needs paired judged coverage, a view that
counts reviewer failures, and results excluding the diagnostic PR.
Medium effort, alternative models, larger budgets and disabled web search
were not evaluated.

Before another quality comparison, run a judge smoke test that exercises
unmatched true and false findings; a match-only smoke test missed the
empty-answer problem here. Keep upstream's classification re-ask, and
consider capturing Gemini's finish reason so provider-side stops like the
one on ardevd/jadx-collaboration#3 can be diagnosed.

Test one prompt change at a time on a development split, then freeze it and
compare against single/low on untouched PRs with repeated paired runs.
Include a separate clean-PR sample and retain failed reviews in the recall
denominator. Have a human check consequential disagreements and apply
suggestions to their exact target lines. Use the development guide's existing
rollout targets rather than choosing thresholds after seeing the results.

The production defaults did not change: paco still runs single-pass review
at low effort. The investigation itself produced the verified-response
envelope fix, plus diagnostics and an optional temporary capture that make
failures inspectable without retaining prompts or tool results. The prompt
and model-loop changes that followed are described in
[Changes made after this report](#changes-made-after-this-report).

## Reproduction and retained evidence

The [development guide](../development.md#reviewbench) describes the local image,
mounts and diagnostics. This comparison used direct Docker invocations rather
than automatic harness retries, with:

```text
RB_CONFIG_MODEL=claude-opus-4-6@default
RB_CONFIG_STRATEGY=single
RB_CONFIG_EFFORT=low   # separately: high
RB_CONFIG_EXPLORATION=true
RB_CONFIG_WEB_SEARCH=true
RB_DIAGNOSTICS=/work/out/diagnostics.json
```

Each invocation used the same image ID, the manifest's exact PR/base/head
identity, a read-only checkout and diff, and a read-only credential-file mount.
Resolve credential paths before changing directories and never put credential
contents in a manifest or report.

The judge command, from the pinned ReviewBench checkout, was run once with
each configuration's findings-only directory:

```shell
npm run judge -- \
  --candidate ../full-comparison/candidates-single-low \
  --golden ./golden --manifest ./corpus/test/test.json \
  --provider google-vertex --model gemini-2.5-pro \
  --repo-dir ../judge-repos-low \
  --output ../full-comparison/judge-single-low/scores.json \
  --concurrency 2
```

For high effort, the three `low` directory suffixes became `high`. The judge
used `GOOGLE_CLOUD_LOCATION=global`; the reviewer used `VERTEX_LOCATION=global`.
Each configuration had separate judge checkouts and output directories.

The session-local judge patch used for the original passes disabled
automatic retries, including the extra malformed-classification repair
call. It used in-memory authentication and sessions and disabled user
extensions, skills and context files. Matching/classification prompts and
scoring rules were unchanged. This isolated the judge and enforced the
paid-call allowance, but is a difference from the unmodified harness. The
retry passes used the repaired patch, which restores the single re-ask.
Local tuning results are not official scores.

The retry passes used the same command with `--concurrency 1`, an output
directory seeded with a copy of the original checkpoint, and an opt-in
`REVIEW_BENCH_CAPTURE_RESPONSES=<private path>` for the temporary captures:

```shell
mkdir -p ../full-comparison/retry-single-low
cp ../full-comparison/judge-single-low/scores.checkpoint.json \
  ../full-comparison/retry-single-low/
npm run judge -- \
  --candidate ../full-comparison/candidates-single-low \
  --golden ./golden --manifest ./corpus/test/test.json \
  --provider google-vertex --model gemini-2.5-pro \
  --repo-dir ../judge-repos-low \
  --output ../full-comparison/retry-single-low/scores.json \
  --concurrency 1
```

| Artifact | Revision or SHA-256 |
| --- | --- |
| ReviewBench corpus | `e1cb1a0dad8105ebea45caa00c194eaf2d2e7b5d` |
| Test manifest | `0f277447878a0b02f587c0b40a76a74985d9d1db3cb212d9dfbc97b1d1270f60` |
| Selected 25-PR golden content | `51431df0bfaed96fbdc6b1800cdff7c4f500a4e4bcdd476c9e85c722f41608cf` |
| paco base commit, with uncommitted adapter changes | `64719e2617009714816bcdf316e0073144ca55a4` |
| Reviewer image | `sha256:9ecd1bd32475248bfcd0671f8058c198c35c3e642560519e03a420b3107e0336` |
| Recorded source digest | `f7d2428aff55ac82647966ad1c3e571612885cd4df4896d20780d7ba62fafcbd` |
| Prompt digest | `b2702d2aceabf922c70f50c54c7f78bda486740866f2058db717fce4bac785de` |
| Local judge patch | `55bdd023f440d805830e8e9d1245e269ed770e18d09bd5193f832db2c19abef2` |
| Repaired judge patch used for retries | `a25d8779a35b4c2d7293a47f59a0ee8d549d408b95dcbdd89e5dd79fba7728be` |

The golden-content hash uses the upstream convention: sort PR keys, join the
raw golden-file contents with a newline, then SHA-256.

The local experiment artifacts include the run ledger, per-run findings and
scrubbed diagnostics, usage logs, failure-placeholder ledgers, judge
checkpoints, source hashes and harness patch. They are session-local and not
bundled into this repository. No credential files, temporary response
captures or judge transcripts are included in the report.

Validation results:

| Command | Result |
| --- | --- |
| `go test -mod=vendor -race ./internal/review/... ./internal/reviewbench/... ./internal/model/... ./hack/paco-reviewbench/...` | Passed |
| `make lint` | Passed after the response-envelope fix |
| `git diff --check` | Passed |
| `npm run typecheck` in the local ReviewBench checkout | Passed |
| `npm run test:judge` in the local ReviewBench checkout | Passed, 31 tests |
| Judge command above, low and high directories | Both failed strict aggregation; 8 and 1 PR classification errors respectively |
| Retry passes with the repaired judge | High passed strict aggregation (25/25); low 24/25, ardevd/jadx-collaboration#3 unjudged |
| Repaired judge: `npm run typecheck`, `npm run test:judge` | Passed, 45 tests |
| Repaired judge: `git diff --check`, `git apply --reverse --check ../judge-repair.patch` | Passed |
| Two-finding live classifier diagnostic | Passed; both labels correct |
