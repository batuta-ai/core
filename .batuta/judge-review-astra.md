# Independent review of how batuta applies Jev — codex gpt-6-astra, 2026-09-22

Read-only codex session `01a0c981-f0fa-73e1-a7c3-37a91bba12cc` (codex-cli 0.154.0, gpt-6-astra, reasoning high, sandbox read-only, 292,834 tokens) on branch `feat/claim-evidence-v3` at `95b9397`. Brief reproduced at the end. Paths in links are absolute to the reviewer's machine.

**Verdict:** Keeping both decisions out of enforcement is justified; treating these results as a demonstrated limit of Jev is not. Classification has overlapping labels and instructions that strongly favor `high`. Claim verification combines incomplete evidence, contradictory criteria, and synthetic labels that are not reliably ground truth. These are substantial application defects, although fixing them does not guarantee Jev will become useful. The frozen failures remain failures. ([Classification rubric](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:16), [claim criteria](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:321), [benchmark verdicts](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:215))

I used local files and offline recomputation only. No files were edited, no judge was run, and no network commands were executed. References to TypeSafe guidance below mean the guidance recorded in your research document, not an independent online verification.

**Numbered findings**

1. **High — The behavior rubric confuses missing support with contradiction.**

   **What:** The state note explicitly says absence from a short slice is not proof of absence. The question asks for *positive evidence that the claim is false*. But `behaviour_absent` means “the diff slice shows the path changed but nothing that does what the claim says.” That last criterion permits precisely the inference the note prohibits. ([Note](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:42), [question](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:282), [criteria](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:321))

   **Why and plausible cost:** Jev can answer whether a supplied passage supports, contradicts, or leaves a claim unresolved. It cannot reliably determine repository-wide behavioral absence from an incomplete preview. For a literal reader—the documented weakness you recorded—this conflicting rubric is a plausible major contributor to false flags. Its exact contribution to the **21/40** cannot be isolated from these runs. ([Research](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:11), [v3 results](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:254))

   **What to do first:** Make the relations mutually exclusive: direct support, explicit contradiction, insufficient evidence. Require a concrete opposing fact for contradiction; reserve absence-based contradiction for evidence code has established as complete. Fix this before tuning another threshold.

2. **High — The 1,800-byte retrieval policy can discard the most relevant evidence entirely.**

   **What:** `DiffSlice` ranks whole hunks by token-occurrence counts and skips any hunk that does not fit. It does not extract a smaller relevant region from an oversized hunk, require a positive relevance score, or restrict retrieval to the claimed file. Thus a large implementation hunk can disappear while smaller, less relevant hunks survive. Each claim receives a slice, and the shared state also contains another slice ranked against all claims. ([Retrieval](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:530), [per-claim evidence](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:391), [shared state](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:292))

   **Why and plausible cost:** This is an evidence-selection problem, not simply “too little context.” Increasing the global budget could help, but does not repair the selection rule. The documented small-state guidance calls for *relevant* bounded evidence; it does not establish 1,800 bytes as sufficient for arbitrary code behavior. ([Research guidance](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:102), [Yoshi evidence design](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:122))

   **What to do:** On a manually adjudicated development set, compare the current slice with a reviewer-selected, sufficient evidence packet containing the relevant function, dependencies when necessary, and matching test evidence. Record retrieval sufficiency separately from Jev correctness. Cross that comparison with the corrected rubric from finding 1. Until then, **21/40 measures this pipeline against its constructed labels; it does not identify a Jev capability ceiling**.

3. **High — `true_behaviour` is an assumed negative, not a verified true claim.**

   **What:** The generator appends:

   > Updated `<first alphabetically sorted changed path>` so that `<task title>`.

   It does not verify that this particular file implements the task’s behavior. A title may describe several files, documentation, tests, or an imperative rather than a proposition about the selected file. Likewise, `behaviour_absent` borrows another delivery’s title without checking that the behavior is actually absent. ([Variant construction](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:258), [donor selection](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:325))

   **Why and plausible cost:** This can bias **against** Jev by labeling an unsupported or misattributed statement true. It can bias **for** Jev by making false claims conspicuously unrelated. Task completion is also inferred from `candidate`, not from independent adjudication of every report sentence. ([Source eligibility](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:166))

   `wrong_diff` introduces another confound: it swaps the diff and changed paths while retaining the original report, proofs, and verifier. A case can therefore flag an original path claim rather than the appended semantic defect. ([Case construction](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:213), [wrong-diff variant](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:279))

   **What to do:** Independently label individual claims and their evidence relationship. Use matched true/false claims about the same behavior and file, including subtle defects. Attribute detection to the injected claim, rather than any contradiction anywhere in the report.

4. **High — The old materiality gate asked a question the corpus omitted the information needed to answer.**

   **What:** The materiality question asks whether the task would be incomplete if the claim were false. But the corpus constructor supplies only the task ID: no title or scope. This omission exists in run 1’s `f2cc89c` constructor at `cmd/batuta/judge_corpus.go:509–516` and remains in the current [constructor](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:581). The [question](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:287) still references `task`.

   **Why and plausible cost:** Even with a task description, a false *extra* claim need not mean the requested task is incomplete. The old policy required both choice confidence and materiality to reach 0.9 (`f2cc89c:loop/judgment.go:1126–1134`). This was an especially restrictive gate around an under-specified question.

   The cost is observable: run 1 contains **33/103 asked behavior cases with contradiction probability ≥0.9**, but zero judge-triggered aggregate flags. One raw case has probability **0.98**, `settled_by:"judge"`, and `flagged:false`. The saved outputs cannot separate the confidence gate’s loss from the materiality gate’s loss. ([Reanalysis](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:236), [raw example](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-corpus-v1/run-core.jsonl:1))

   **What to do:** Keep materiality out of the contradiction detector. If needed for action policy, ask whether a verified discrepancy affects a *named acceptance criterion*, with that criterion supplied.

5. **Medium — Several original TypeSafe departures were fixed; `noul` itself was not the fundamental mistake.**

   The version history supports this assessment:

   | Dimension | Assessment and plausible cost |
   |---|---|
   | Atomic decisions | V1 asked global contradiction questions over an attempt. Current code asks one relation per extracted claim: substantial improvement. V1’s 0/2 detections at 0.9 cannot be attributed to question type alone. |
   | Code computes mechanical facts | Correct direction. V2.3 caught both known false closures through code and flagged 0/27 candidates. Jev added no contradictions on that replay. |
   | Structured questions | Current JSON state and `claims.cN.evidence` references follow the recorded guidance. A string instruction pointing to structured keys is not inherently a violation. |
   | Choice versus `noul` | Choice is appropriate for support/contradiction/unknown. But your Yoshi notes explicitly describe successful Boolean-question designs; “no choice confidence” does not by itself invalidate `noul`. |
   | Confidence gating | V2’s additional materiality gate suppressed action; v3 now thresholds summed defect probability. Neither score should be assumed calibrated merely because it is numerical. |
   | Escape options | Claims have `unverifiable`, though its meaning conflicts with `behaviour_absent`. Classification has no explicit insufficient-information option. |

   Evidence: [recorded TypeSafe guidance](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:99), [Boolean Yoshi design](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:125), [v2.3 results](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:139), [current aggregation](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:1103), [classification options](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:30).

   **What to do:** Preserve the atomic, structured, code-first architecture. Evaluate question wording, evidence sufficiency, and action thresholds separately. Do not replace one unvalidated double gate with another merely because another project uses two questions.

6. **High — Classification is strongly biased toward `high`, but the write-up overstates the certainty of that diagnosis.**

   **What:** The instruction says “a self-sufficient brief is high.” The `high` criterion also includes any “multi-file work fully captured by a precise brief.” These overlap with isolated features, clear bugs, and simple tests whenever source and test files appear together. ([Instructions and rubric](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:16))

   A concrete example: a contained commit-title formatting fix touches implementation plus tests and is labeled `low`; the pipeline returned `high` with **0.90 confidence**. Both labels are defensible under different parts of the supplied rubric. ([Task](/Volumes/Home/francisross/Projects/batuta/core/.batuta/plans/done/review-proofs.md:8), [answer](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-classify-v1/run.jsonl:107))

   **Why and plausible cost:** My leading explanation is **rubric plus question framing**, with context selection a secondary possibility. The state contains task title, scope, acceptance criteria, and up to 4,000 bytes of selected context—not repository inspection or executor capability evidence. That limits what “difficulty” can mean. ([State construction](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:84), [context bound](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:152))

   However, “the rubric, not the judge’s ability” is a causal conclusion without an ablation. Jev might still perform poorly with a better rubric. Also, **171 high / 10 critical are post-policy outputs**: the ten `critical` results are low-confidence fallbacks, and their original choices were discarded. ([Benchmark interpretation](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:246), [fallback mapping](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:113), [recording](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_classify.go:165))

   **What a fair second run looks like:** Define mutually distinguishable work categories, explicitly stating that ordinary source-plus-test changes do not imply `high`. Separate work complexity from brief completeness; let code combine them into routing policy. Add `insufficient_information`. Use independently adjudicated labels, development and untouched evaluation plans, and a paired comparison of old versus revised questions. Preserve original choices, full distributions, abstentions, and final fallback lanes separately. Include genuinely critical cases; run 1 had none. ([Label distribution](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:195))

7. **Medium — The frozen rules are legitimate deployment gates, but not clean model-capability tests.**

   **What:** Freezing thresholds, reporting negative results, and stopping before the test half are good experimental discipline. The classification rule also checks a constant baseline and under-routing, rather than accuracy alone. ([Frozen rules](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:188), [classification rules](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:204), [v3 procedure](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:224))

   **Limitations and cost:**

   - **Small-sample cutoff:** With 80 calibration negatives, 2% allows at most one false flag. Two produce 2.5% and fail. At 0.95 the positive detection total is **40/80**, while the negative total is **2/80**. That is a valid frozen failure, but a one-case boundary—not evidence of a precisely established population error rate. ([Core sweep](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-corpus-v3/cal-core.json:10), [skills sweep](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-corpus-v3/cal-skills.json:10))
   - **Negative dilution:** Pooling 40 mostly easy `clean` cases with 40 `true_behaviour` cases reports 2.5%, while the behavior-negative stratum is **2/40 = 5%**. The mixture should reflect intended usage, with both strata reported.
   - **Incomplete independence:** Splitting by attempt keeps its variants together, which is good. But tasks from one delivery can cross halves, and donor titles/diffs are selected before any split restriction. Therefore source material can cross the boundary. ([Split](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:413), [donor selection](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:325))
   - **Easy code positives:** Fabricated names are deliberately absent, and wrong counts are generated using the same counting shape as settlement, plus three. Their 100% detection validates this construction, not arbitrary real-world reporting errors. ([Fabrication](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:290), [counting](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:399))

   **What to do:** Keep the recorded verdict unchanged. For the next registration, justify error budgets from retry/review costs, report uncertainty and per-stratum performance, split by delivery or project, and select donors within each partition.

8. **High — Calibration silently loses availability information required by the frozen protocol.**

   **What:** `corpusRunCases` records unavailability, but calibration reduces each result to only its case, code flag, and maximum probability. It includes every case in the denominator and emits no availability count. An unavailable, otherwise unflagged negative therefore lowers the measured false-flag rate. The frozen rule requires unavailable cases to be excluded and a repeat above 10%. ([Reduction](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:885), [denominator](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:905), [required rule](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:231))

   **Why and plausible cost:** This could select a threshold using missing judgments. I cannot establish that it affected this run: the saved calibration files contain aggregate sweeps, not the per-case availability evidence needed to check. ([Saved output shape](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:973))

   **What to do:** Retain and report availability before selecting thresholds. Save per-case requests, responses, extracted claims, retrieval coverage, and separate code/model flags. Also fix run summaries before testing v3: `wrong_diff` is missing from the defect-label map, and false flags are counted only for `clean`, not `true_behaviour`. ([Summary implementation](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:672))

9. **Medium — Corpus/live parity and reproducibility remain weaker than the prose implies.**

   **What:** Live extraction supplies a known-path filter; corpus extraction still omits it. The benchmark already identified that omission as a cause of false code flags. ([Live extraction](/Volumes/Home/francisross/Projects/batuta/core/loop/judgment.go:442), [corpus extraction](/Volumes/Home/francisross/Projects/batuta/core/cmd/batuta/judge_corpus.go:600), [observed failures](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:182))

   Transport uses the native typed request shape and validates answer keys/types/options; I found no evidence that an incorrect endpoint or chat wrapper explains these results. However, `auto` forbids a configured model, while TypeSafe defaults to `jev-latest`; the corpus output does not retain the returned model. Future benchmark reproducibility therefore needs stronger version recording. ([Transport](/Volumes/Home/francisross/Projects/batuta/core/judge/http.go:100), [validation](/Volumes/Home/francisross/Projects/batuta/core/judge/http.go:183), [auto configuration](/Volumes/Home/francisross/Projects/batuta/core/judge/config.go:179), [default model](/Volumes/Home/francisross/Projects/batuta/core/judge/http.go:21))

   **What to do:** Use the same request-building path for live and corpus evaluation, pin the benchmark model, and retain exact evidence artifacts. Hashes alone cannot restore lost corpus inputs—the benchmark itself reports 41 unresolved attempts in v3. ([Artifact loss](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:252))

10. **Medium — Several benchmark conclusions need narrower wording or corrected numbers.**

   **What and corrections:**

   - **Run 1 understates available signal.** Offline recomputation reproduces **33 cases ≥0.90** and AUC **0.96836**. The earlier discussion’s 18-case argument does not describe all high-probability cases. Neither count rescues the frozen 50% rule. ([Earlier discussion](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:181), [reanalysis](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:238))
   - **“It measured suspicious-claim presence” is plausible, not demonstrated.** The 103 asked positives versus 27 asked clean cases create a strong confound, but the AUC does not identify the feature Jev used. ([Reanalysis](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:236), [later interpretation](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:269))
   - **V3 was not calibrated on held-out test data.** It selected—or failed to select—on the calibration half; the test half was never run. Call this a failed threshold-selection stage, not a held-out performance result. ([Procedure and headline](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:267))
   - **“Nearly as readily” obscures separation.** At 0.50, behavior positives flag **36/40 = 90%**, versus **21/40 = 52.5%** for behavior negatives. That is unacceptable specificity but meaningful separation. At 0.95, the corresponding counts are 21 and 2. ([Table](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:256))
   - **Classification outcomes show no demonstrated need for up-routing, not a measured cost penalty.** The 59 lower-lane first-attempt successes support that restrained statement. They do not measure counterfactual quality, latency, or spend under the proposed lane. ([Outcome table](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:222))
   - **“Cheap, fast, calibrated judgment” is premature.** Your own research records failed calibration elsewhere, while these experiments select operating thresholds rather than establish probability calibration. ([Initial claim](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:80), [calibration evidence](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:158))

   **What to do:** Report three distinct results: extraction/evidence coverage, model discrimination on adjudicated claims, and final policy performance. Preserve the negative deployment verdict without turning it into a broader model verdict.

**Ranked recommendations**

1. **Provider quota/rate-limit message classification.** This is my strongest candidate: the decisive evidence can be present in one short message, and the existing implementation already documents confusion between real provider limits and test output mentioning `429`. Collect real terminal messages from each adapter, hard negatives containing quoted errors/test fixtures, and a provider- or time-separated evaluation set. Measure incremental recovery over the existing regex; keep reset-time computation in code. ([Existing decision](/Volumes/Home/francisross/Projects/batuta/core/executor/run.go:166), [reset parsing](/Volumes/Home/francisross/Projects/batuta/core/executor/run.go:217))

2. **Environment objection versus substantive verifier finding.** Ask about one `INCOMPLETE` explanation, with its criterion and proof result, using `environment_only`, `substantive_gap`, `mixed`, and `unclear`. Collect examples containing misleading words such as “permission” or “unverified,” and adjudicate whether setting the objection aside would hide a defect. Start as an annotation or stricter review signal: today the regex can set aside an objection when the proof passed. ([Current rule](/Volumes/Home/francisross/Projects/batuta/core/gates/gates.go:407))

3. **Question triage and notification priority.** Classify one explicit executor question as an unresolved user decision, environmental problem, already documented instruction, or unclear. Collect real question/answer pairs and the exact relevant plan passage; measure missed human decisions and unnecessary interruptions. Keep answering authority with the existing policy/operator. This requires less repository inference than behavioral verification, while matching an existing structured question entry point. ([Question extraction](/Volumes/Home/francisross/Projects/batuta/core/executor/run.go:186), [proposed policy boundary](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:49))

---

## Brief

You are reviewing, read-only, how the batuta core applies Jev (TypeSafe System One, model jev-1.13.0) as a judge. Do not edit any file. Do not run the judge or any network command. Reading files and running `go test` or `grep` is fine.

Context: batuta conducts coding tasks through executors (other CLIs) and verifies them with gates. We tried to use Jev for two decisions and both failed their pre-registered rules. The maintainer has read many good articles about Jev and asks whether we are applying it correctly. We want an independent second opinion, not agreement.

Read, in this order:
1. `.batuta/judge-research.md`: sections 1–9 (what Jev is, the TypeSafe guidance we followed, compozy/yoshi, BargLabs, GitHub survey) and 10–12 (frozen decision rules).
2. `.batuta/judge-benchmark.md`: every replay and run, the post-hoc reanalysis, and "claim_evidence v3, calibration".
3. The code: `judge/` (transport, config), `loop/claims.go` (claim extraction and code settlement), `loop/judgment.go` (state, questions and criteria sent to Jev, aggregation), `classify/classify.go` (plan-task lane classification), `cmd/batuta/judge_corpus.go` (corpus variants and calibration).
4. Raw outputs: `.batuta/judge-corpus-v1/`, `.batuta/judge-corpus-v3/`, `.batuta/judge-classify-v1/run.jsonl`.

Answer these, citing file:line or a benchmark number for every claim:
1. Where does our use of Jev depart from how TypeSafe says to build with it (atomic decisions, code computes what code can, structured questions, choice vs noul, confidence gating, escape options, state size)? Name each departure and how much it plausibly cost us.
2. claim_evidence: is the question we ask Jev (state keys, instructions, criteria, the 1,800-byte diff slice) one it can answer? Is the `true_behaviour` false-flag rate (21/40 at 0.50) a Jev limit or a limit of the evidence we give it? What would you change first?
3. Classification: the judge answered `high` for 171/181 tasks. Is that the rubric, the state, the question shape, or Jev? What would a fair second run look like?
4. Are the corpus and the frozen rules fair tests? Anything biased for or against the judge (variant construction, the split, thresholds, the 2% bar, the negatives)?
5. Is there a decision in batuta where Jev is more likely to earn its place than these two? Rank at most three, with the evidence you would collect first.
6. Anything in the benchmark write-ups that overstates or understates what the data shows.

Write your answer as a structured report in English: a short verdict paragraph, then numbered findings (severity: high/medium/low, file:line or number, what, why, what to do), then the ranked recommendations. Be direct; disagree with our conclusions where the evidence supports it.
