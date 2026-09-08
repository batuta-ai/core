---
name: codex
run: fake-reviewer --write {brief}
readonly: fake-reviewer --read-only {model_flags} {prompt}
model_flags: --model {model}
available: fake-reviewer --version
models: fake-reviewer models
finished: exit_code
limit_regex: usage limit reached
---
