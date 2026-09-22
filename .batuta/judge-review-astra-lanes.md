# Independent review of lane model choice and Jev for routing — codex gpt-6-astra, 2026-09-22

Read-only codex session `01a0c9b0-4e2b-7e91-8546-894a9abed580` (codex-cli 0.154.0, gpt-6-astra, reasoning high, sandbox read-only, 146,193 tokens) on `main` at `a85a9cc`, with the skills repository and `batuta inventory` output of 2026-09-22 readable. Brief reproduced at the end. Paths in links are absolute to the reviewer's machine; the inventory path points to a temporary job directory that no longer exists.

**Verdict:** Keep the current lane assignments provisionally: Agy/Gemini Flash low, OpenCode/GLM Flash medium, Cursor/Grok high, and `critical=self`. The records support these as working defaults, not proven optimal choices. Jev could plausibly classify a narrowly defined task characteristic from supplied evidence; it has not demonstrated that it can select the best executor/model. Prefer a deterministic table now, followed by a small matched experiment—not an adaptive bandit yet. Jev’s existing classification pipeline scored **44.2% against a 46.4% constant baseline**. [Benchmark:195](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:195)

I used local files and offline calculations only. No files were edited, and no executors, judge calls or network commands were run.

The following table is computed from **all 59 JSONL journals** in the requested [core](/Volumes/Home/francisross/Projects/batuta/core/.batuta/journal) and [skills](/Volumes/Home/francisross/Projects/batuta/skills/.batuta/journal) directories.

Counting rules:

- A task-run is one **repository/delivery/task** with an `executor_started` event: **162 task-runs**, comprising 146 core and 16 skills.
- “First pass” means `candidate_recorded` on its first execution. Questions, interruptions and abandoned runs remain in the denominator. `already_satisfied` is reported separately.
- Retries count **actually launched same-runtime retries following failure**, excluding question continuations and restarted deliveries.
- Time comes from `executor_finished.duration_ms`: worker session time, including failed sessions, but excluding gates, verifier, human waits and integration. Missing finishes remain unknown.
- Lane means the task’s authored lane; escalation-only executions are listed below.

| Lane | Executor / model | First pass | Candidate executions / launches | Failure retries launched | Escalations or handoffs | Worker time: median; sum; finished sample |
|---|---|---:|---:|---:|---|---|
| low | agy / `gemini-3.8-flash-low` | **4/7 = 57.1%** | 5/8 | 1 | 0 | 86.0 s; 14.5 min; n=8 |
| low, historical | codex / `gpt-5.4-mini` | **5/6 = 83.3%** | 5/7 | 1 | 1 launched → Sol | 109.0 s; 23.5 min; n=7 |
| medium, historical | codex / `gpt-5.6-sol` | **43/60 = 71.7%** | 50/74 | 9 | 4 launched → Astra | 459.5 s; 675.7 min; n=73 |
| medium | opencode / `opencode/glm-5.3-flash` | **17/24 = 70.8%** | 22/31 | 7 | 1 scheduled → Grok; **never launched** | 579.9 s; 337.9 min; n=30 |
| high, historical | codex / `gpt-6-astra` | **20/46 = 43.5%** | 31/69 | 7 | 1 scheduled → self; 1 separate `needs_conducting_session` | 385.8 s; 503.6 min; n=67 |
| high | cursor-agent / `cursor-grok-4.6-high` | **14/19 = 73.7%** | 15/22 | 3 | 2 `needs_conducting_session` | 915.7 s; 294.8 min; n=19 |

**Escalation-only observations:** Sol additionally handled one originally-low task: one launch, one candidate, **381.1 seconds** (`followups-20260906-164938/task_1/e3`). Astra handled four originally-medium task-runs: **five launches, four finishes, two candidates**, median **231.1 seconds**, total **817.0 seconds**. These were selected after lower-route failures, not comparable initial assignments.

**Restarts matter.** Collapsing to the earliest execution of each repository/plan-slug/task leaves **135 distinct task identities**. First-pass counts become Agy **4/7**, Mini **5/6**, Sol **36/52**, Astra **14/32**, GLM **16/23**, and Grok **12/15**. Neither denominator supplies independent, randomized model comparisons.

**Agy’s two apparent successes were false closures.** Both `research-ladder-20260920-001419` runs—core task 3 and skills task 4—recorded `already_satisfied`, subsequently identified as false. Counting them would inflate Agy’s first-pass figure to **6/7**. The defensible result is **4 first-attempt candidates, 2 false closures, 1 no-change failure later recovered**. [Benchmark:9](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:9), [benchmark:29](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:29)

**Cost is unknown for every worker row.** Across **217 launches and 209 finishes**, I found no worker usage or billing receipts. The **16 usage-bearing records** are `judge_result` events totaling **28,065 input and 1,032 output tokens**; they are not executor consumption. “Free quota,” “credits, cents,” and “subscription” are routing descriptions, not measured per-task costs. CLI token reporting is explicitly absent from the loop’s documented contract. [Routing:9](/Volumes/Home/francisross/Projects/batuta/core/.batuta/routing.md:9), [loop:431](/Volumes/Home/francisross/Projects/batuta/core/docs/loop.md:431)

My lane recommendations are therefore conservative:

| Lane | Recommended assignment | Evidence | Risk |
|---|---|---|---|
| **low** | **agy / `gemini-3.8-flash-low`**, provisionally | Listed in today’s inventory; 4/7 first-pass candidates and one successful retry. A contained implementation/test change succeeded in `review-proofs/task_1`. [Inventory:26]( /Volumes/Home/francisross/.claude/jobs/ba13ca57/tmp/inventory.txt:26), [WORK:174](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:174) | Weakest recommendation: only seven tasks and two known false closures. Retain the independent gates and require task-specific proof of requested changes; do not broaden its scope based on the nominal free quota. |
| **medium** | **opencode / `opencode/glm-5.3-flash`** | Listed inventory model; 17/24 first-pass candidates, 22 candidates overall. Seven retries, five recovered to candidates. [Inventory:1595](/Volumes/Home/francisross/.claude/jobs/ba13ca57/tmp/inventory.txt:1595) | All 24 observations are core tasks; most concern the judge workstream. No measured price advantage or cross-project reliability claim is available. |
| **high** | **cursor-agent / `cursor-grok-4.6-high`** | Listed inventory model; 14/19 first-pass candidates, 15 eventual candidates. Complies with the Grok-only constraint. [Inventory:466](/Volumes/Home/francisross/.claude/jobs/ba13ca57/tmp/inventory.txt:466), [routing:5](/Volumes/Home/francisross/Projects/batuta/core/.batuta/routing.md:5) | Three launches lack finish durations; two runs reached a conducting-session handoff. The 915.7-second median is descriptive, not proof that Grok is slower than another model on equivalent tasks. |
| **critical** | **Keep `self`** | This preserves the conducting-session boundary. The loop explicitly rejects directly routed `self` tasks and hands off escalation to it. [Loop:199](/Volumes/Home/francisross/Projects/batuta/core/docs/loop.md:199) | It interrupts unattended execution. Replacing it with an external model would change the workflow contract, not merely upgrade model capability. |

**Do not restore Codex:** its removal on **2026-09-20** was an explicit capacity decision. The absence of recorded quota failures here does not invalidate that constraint. [Routing:5](/Volumes/Home/francisross/Projects/batuta/core/.batuta/routing.md:5)

**Do not promote an untested alternative from its name alone.** Claude declares `fable`, `haiku`, `opus` and `sonnet`; Cursor lists Grok 4.7; OpenCode lists GLM 5.3, newer Claude models and other alternatives. There are **zero implementation launches for those alternatives in these journals**. Claude/Fable review evidence in WORK is a different workload. There is **not enough data** to recommend any of them over the current defaults. [Inventory:123](/Volumes/Home/francisross/.claude/jobs/ba13ca57/tmp/inventory.txt:123), [inventory:572](/Volumes/Home/francisross/.claude/jobs/ba13ca57/tmp/inventory.txt:572), [inventory:1567](/Volumes/Home/francisross/.claude/jobs/ba13ca57/tmp/inventory.txt:1567), [WORK:307](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:307)

1. **High — Static lane assignment is primarily an empirical policy decision.**

   **What:** Jev can plausibly judge semantic compatibility between a task family and supplied capability evidence. It cannot discover current executor competence, quota, prices or repository requirements by itself. Your research describes a typed decision model that cannot inspect repositories or run commands, and explicitly excludes arithmetic and routing-table decisions from its intended responsibilities. [Research:7](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:7), [research:55](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:55)

   **Why:** Choosing a rarely changed lane default requires an objective: acceptable failure risk, completion time, monetary spend, subscription capacity and operator intervention. None can be inferred reliably from model identifiers.

   **What to do:** Have code compute comparable outcome counts, uncertainty, failure categories, total attempt consumption and eligibility. Review the resulting small table periodically.

   If Jev participates, give it one lane/task-family decision at a time, with:

   - Explicit requirements and independently adjudicated example tasks.
   - Eligible executor/model/version/effort combinations.
   - Relevant, dated capability evidence and failure examples.
   - Code-computed sample sizes, outcome estimates and uncertainty.
   - Operator constraints and explicit unknowns.

   A plausible question is: **“Which candidate’s supplied capability evidence directly covers this task family’s stated requirements: A, B, neither, or insufficient evidence?”** Code should perform the final constrained optimization. If all useful inputs are already numeric, Jev adds no demonstrated value.

2. **High — Dispatch-time selection is a different problem, and the existing classifier does not solve it.**

   **What:** The current state contains title, scope, acceptance criteria and bounded context—not executor capability or comparative outcome evidence. Its overlapping rubric says a self-sufficient brief is high and also makes multi-file work high. [classify.go:20](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:20), [classify.go:30](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:30), [classify.go:84](/Volumes/Home/francisross/Projects/batuta/core/classify/classify.go:84)

   **Why:** The **171 high / 10 critical** results are final policy outputs; the ten critical results were confidence fallbacks. The run had **zero critical labels**, and **59 tasks proposed for upward routing already passed first attempt at their executed lower lane**. This shows no demonstrated need for those changes; it does not measure their counterfactual cost. [Benchmark:195](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:195), [benchmark:222](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:222), [earlier review:76](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-review-astra.md:76)

   **What to do:** If semantic dispatch assistance is tested, ask an atomic question such as: **“Does the supplied task require changing interacting behavior across components, a local mechanical edit, or is the evidence insufficient?”** Provide the relevant contract, acceptance criteria and bounded dependency evidence. Keep path counting, quota handling, eligibility, scoring, tie-breaking and fallback selection in code.

   Fixing the rubric might help. **There is no ablation demonstrating that it will.** The earlier review correctly treats rubric failure as a hypothesis, not an exoneration of Jev. [Earlier review:68](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-review-astra.md:68)

3. **High — These rates measure the executor–brief–harness combination, not isolated model ability.**

   **What:** Astra had **21/46 first executions ending in questions**, versus **2/19 for Grok**. Some historical failures were verifier/environment problems: WORK explicitly records a sandbox veto later resolved by the conductor. GLM’s `judge-claims-v21/task_3` required journals unavailable inside its worktree and was ultimately handled by the host. [WORK:54](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:54), [benchmark:95](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-benchmark.md:95)

   **Why:** Different tasks, harness revisions, permissions, verifier routes and human interventions confound model comparisons. A candidate also precedes independent delivery review. WORK contains duplicate “ticked before run” entries and at least one “first attempt” completed partly by the conductor. [Loop:296](/Volumes/Home/francisross/Projects/batuta/core/docs/loop.md:296), [WORK:50](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:50), [WORK:56](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:56)

   **What to do:** Preserve these distinctions in any routing dataset. Do not conclude that Grok beats Astra, GLM matches Sol, or Agy’s failures establish a general capability limit. Keep implementation acceptance, subsequent review defects, environmental failures and human assistance as separate fields.

4. **Medium — A simple table is justified; a contextual bandit is premature.**

   **What:** Most observed models occupy different lanes. The limited cross-model observations are escalation cases selected after failure. For example, Sol rescued Mini’s `followups/task_1`; Astra rescued Sol’s `roadmap/task_1` and `watch-style/task_2`. They are useful recovery evidence, not randomized comparisons. [WORK:64](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:64), [WORK:77](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:77), [WORK:110](/Volumes/Home/francisross/Projects/batuta/core/WORK.md:110)

   **Why:** A bandit cannot reconstruct the outcomes of unchosen models from these logs. It would also optimize an unreliable reward if false closures counted as successes or unknown spend counted as zero.

   **What to do:** Keep a versioned table with explicit eligibility and operator constraints. Collect controlled overlap before adaptive exploration. Any later selector must preserve the chosen route across same-runtime retries and record its decision inputs. Today routing is frozen into a generation, and plan model hints do not override the table. [Loop:48](/Volumes/Home/francisross/Projects/batuta/core/docs/loop.md:48), [loop:173](/Volumes/Home/francisross/Projects/batuta/core/docs/loop.md:173)

5. **Medium — The cheapest useful experiment is a small matched screening test, with a frozen stopping rule.**

   **Proposal, not an experiment run here:** Test the low/medium boundary first using the two existing routes: **Agy/Gemini Flash low versus OpenCode/GLM Flash**. This avoids spending the initial experiment on wholly unobserved models.

   Freeze before execution:

   - **Inputs:** 12 unseen tasks from distinct deliveries, six low and six medium, with sufficient briefs and independently checked acceptance criteria. Keep development deliveries separate. Pin snapshots, briefs, executor versions, models, permissions, transport and timeouts; run each arm in a fresh session.
   - **Policies:** Compare the current lane table; one explicit code rule; and that same rule augmented by one Jev semantic classification. For example, the code rule chooses Agy only for at most two explicit file paths with mechanical proofs for every criterion; otherwise GLM. Jev may flag interacting behavioral requirements and move that choice to GLM. Unclear/unavailable answers retain the code rule.
   - **Judge contract:** Freeze wording, evidence builder, pinned Jev version and an operating threshold—say **0.90**, explicitly experimental rather than calibrated. Save raw choices, distributions, abstentions, latency and usage separately from final routes.
   - **Execution:** **24 initial worker sessions**, one per task/arm, with arm order randomized. Judge recommendations must be recorded before worker outcomes. Evaluate both outputs with identical gates and blind acceptance review. No automatic integration is needed for this screening test.
   - **Primary rule:** Jev proceeds to a larger pilot only if it produces **no additional acceptance failures**, improves at least **two decisions** over the code rule, and reduces total selected-arm time by **at least 20%**, including judge overhead. A “better decision” means selecting the passing arm when the other fails, or the faster arm when both pass. Report all paired outcomes.
   - **Availability:** Report unavailable judgments separately; fallback remains part of end-to-end policy performance. Above **10% unavailable**, repeat the judge phase once and retain both runs, following the existing experimental discipline.
   - **Stop:** No threshold changes after inspection. No improvement means retain the table. Missing billing means **no monetary-savings conclusion**. Twelve pairs are a screening result, not production validation; a subsequent delivery pilot must include retries, escalations, verifier cost and review defects.

   This follows the existing frozen-rule approach while adding the missing counterfactual worker outcomes. A shadow-only replay of current logs is cheaper, but cannot establish that the unchosen model would succeed. [Research:188](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:188), [research:204](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-research.md:204), [measurement:142](/Volumes/Home/francisross/Projects/batuta/core/docs/dispatch-measurement.md:142), [measurement:196](/Volumes/Home/francisross/Projects/batuta/core/docs/dispatch-measurement.md:196)

6. **Low — Model selection is still a lower-priority Jev experiment than localized text triage.**

   Provider-limit classification, verifier environment objections and question triage have decisive evidence that can fit in a short state. Model selection additionally needs comparative capability and outcome evidence that is currently missing. The previous review’s ranking remains justified; none of those applications is validated merely by being a better fit. [Earlier review:124](/Volumes/Home/francisross/Projects/batuta/core/.batuta/judge-review-astra.md:124)

---

## Brief

You are advising, read-only, on model selection for the batuta core's routing lanes. Do not edit any file, do not run executors, the judge or any network command. Reading files and running `grep`, `jq`, `git log` or `go test` is fine.

Context: batuta routes each plan task to a lane (`low`, `medium`, `high`, `critical`) and each lane to an executor CLI and model, from the routing table `.batuta/routing.md`. The loop runs one fresh executor session per task, gates the result, retries on the same executor, then escalates one row. The maintainer asks two things:
A. Would it make sense to use Jev (TypeSafe System One, the judge the batuta already integrates) to decide which model each lane uses, or which model to call for a given task?
B. Independently of Jev: given the executors and models available on this machine and the outcomes the batuta has recorded, which model should each lane call?

Read first:
1. `.batuta/routing.md`, `.batuta/profile.md`, `docs/dispatch.md`, `docs/dispatch-measurement.md`, `docs/loop.md` (routing, escalation, costs).
2. The available executors and models: `/Volumes/Home/francisross/.claude/jobs/ba13ca57/tmp/inventory.txt` (output of `batuta inventory` today).
3. Outcomes: `.batuta/journal/*.jsonl` (per attempt: executor, model, lane, outcome, retries, escalations, timing, usage where recorded) and `WORK.md` (the Done list names executor/model per task and whether it passed first time). Also `../skills/.batuta/journal/`.
4. Jev evidence so far: `.batuta/judge-research.md` (all sections), `.batuta/judge-benchmark.md` (every run, including plan classification run 1 where Jev picked a lane from task text and answered `high` for 171/181 tasks), `.batuta/judge-review-astra.md` (your own earlier review, which ranked provider-limit classification, verifier environment objections and question triage as better uses).

Answer, citing file:line, journal delivery/task or a number for every claim:
1. Is "choose the model per lane" a decision Jev can plausibly make well? What would it need in its state, what would the question look like, and what would code have to compute instead? Distinguish (a) static lane-to-model assignment, which changes rarely, from (b) per-task model choice at dispatch time.
2. What does the recorded outcome data already say per executor/model and lane: first-attempt pass rate, retries, escalations, time, cost where known? Build the table from the journals; state sample sizes and what cannot be concluded.
3. Recommend a model per lane (`low`, `medium`, `high`, and whether `critical` should stay `self`) from the inventory, with the evidence and the risk of each choice. Note constraints: codex was removed from routing on 2026-09-20 because the ChatGPT usage limit is too low; cursor-agent runs Grok only by the maintainer's choice; agy, claude, cursor-agent and opencode are installed.
4. If per-task model choice is worth pursuing, is Jev the right tool, or is a code rule over recorded outcomes (a bandit or a simple table) better? Describe the cheapest experiment that would tell, with a frozen decision rule in the style of `.batuta/judge-research.md` sections 10–12.

Write a structured report in English: a short verdict paragraph, the outcome table, the lane recommendations with evidence and risk, then numbered findings (severity, file:line or number, what, why, what to do). Be direct; say "not enough data" where that is the honest answer.
