---
name: check
description: Run the same checks CI runs on substrate-console before calling a change done. Use after any code change, before committing.
---

From `substrate-console/`:

1. If anything under `web/src` changed, rebuild the view so `web/dist`
   matches: `cd web && npm run build && npx tsc --noEmit -p . && cd ..`.
2. `go vet ./...`
3. `go test -race ./...`
4. `git status --short web/dist`. Rebuilt files there belong in the commit.
   CI fails when `web/dist` differs from a fresh build.

Report what you ran and what it printed. If a check fails, fix the cause;
never skip or weaken a test to get it passing.
