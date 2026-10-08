# `batuta council` — a plan judged by a council before approval

`batuta council --plan <file>` asks several routed executors, through their
read-only adapter lines, whether a plan is ready for the maintainer to approve.
It produces evidence and an exit code. It never approves a plan: approval stays
with the maintainer.

```text
batuta council --plan <file> [--parallel N] [--timeout <duration>] [--out <dir>]
```

`--parallel` bounds concurrent sessions in a stage (default 1). `--timeout`
bounds each session (default 10 minutes).

## Stages

1. **Critique.** Each counsellor reads the plan alone, with the profile's
   convention templates, and prints findings (task, severity, claim, fix) and a
   verdict, APPROVE or REVISE.
2. **Cross-review.** Each counsellor whose critique parsed receives the other
   critiques under anonymous labels (A, B, C…), marks each finding AGREE or
   DISAGREE and ranks the critiques after a `FINAL RANKING:` line. A
   counsellor never ranks or votes for its own critique.
3. **Aggregate.** Matching findings merge by task and claim; support counts the
   distinct counsellors that raised or agreed with a finding. Each critique gets
   an average rank over the rankings it received. The recommendation is REVISE
   when most critiques say REVISE or a blocker has majority support; otherwise
   APPROVE.
4. **Synthesis.** A chairman writes a synthesis for the maintainer from the
   aggregate and the critiques, without the counsellors' identities.

Every session is read-only. Batuta compares the repository tree before and after
each session and aborts with exit 1 if one changed it.

## Role rows

The council is read from `.batuta/routing.md`:

```text
| Role     | Lane | Executor | Model       |
|----------|------|----------|-------------|
| council  | —    | claude   | claude-opus-5-5 |
| council  | —    | codex    | gpt-5.6-sol |
| chairman | high | codex    | gpt-6-sol   |
```

Without `council` rows the members are the distinct non-`self` rows of the low,
medium and high lanes. Without a `chairman` row the chairman is the high lane
row. At least two counsellors are required.

## Artefacts

Under `.batuta/councils/<date>-<plan slug>/` (`--out` overrides), where the slug
is the plan file name without its extension and any `plan-` prefix:

- `council.json` — plan digest (`sha256:…`), critiques, cross-reviews, aggregate
  (findings with support, rankings, recommendation), chairman synthesis,
  failures and the label map from anonymous labels to executor and model.
- `council.md` — recommendation, chairman synthesis, findings with support, the
  ranking table and failures. The command prints the same text.

Batuta refuses to write over tracked files. A session that fails (timeout, rate
limit, unparsable answer) is listed under failures and the council continues
with the rest.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | The aggregate recommends APPROVE. |
| 2 | The aggregate recommends REVISE. This is a result, not a failure. |
| 4 | Fewer than two critiques parsed; the report is marked INCOMPLETE. |
| 1 | An error before a report: bad flags, missing plan, routing or profile, a tree change. |

An APPROVE recommendation is not an approval. The `batuta-council` skill reads
`council.md` and helps the maintainer decide.

## Design source

The design follows Andrej Karpathy's
[`llm-council`](https://github.com/karpathy/llm-council) (commit `92e1fcc`, no
licence declared): independent first opinions, anonymized peer review with a
`FINAL RANKING:` list and an averaged rank, and a chairman synthesis. Only the
design is adopted; no text, prompts or code are copied. Batuta narrows it to one
question, whether a plan is ready to approve, and runs the counsellors headless
through their read-only lines.
