# CLAUDE.md

<!-- section:coding-guardrails -->

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

## 10 Commandments for Go + Kubernetes Repos

1. Design for interfaces, not implementations
Define behavior through Go interfaces. Controllers, reconcilers, and plugins should depend on abstractions so new backends or strategies can be swapped in without touching core logic. If it's hard to test without mocking, the abstraction is missing.

2. Keep packages small and purpose-driven
One package, one concern. Avoid god packages like `util` or `helpers`. Use the `internal/` directory to enforce hard boundaries between public API and implementation details. Consumers of your package should only see what they need.

3. Treat CRDs and APIs as contracts
Version your CRDs from day one (`v1alpha1` → `v1beta1` → `v1`). Never remove or rename fields without a deprecation cycle. Use kubebuilder markers for validation so the API server rejects bad input before your reconciler ever sees it.

4. Make local development a one-command affair
A new contributor should be able to clone, run `make dev` or similar, and have a working local cluster with the controller running in under 10 minutes. Use `kind` or `envtest`, not assumptions about pre-existing infrastructure.

5. Write reconcilers that are truly idempotent
Every reconcile loop must produce the same result when run N times on the same input. No side effects that compound. Lean on server-side apply. Log the reason for every meaningful state transition — your future self at 2am will thank you.

6. Make errors explicit and actionable
Wrap errors with context using `fmt.Errorf("...: %w", err)`. Return sentinel errors or typed errors for callers that need to branch on them. Never swallow errors silently. Log at the callsite that decides to handle, not everywhere in between.

7. Write tests that prove behavior, not structure
Unit test business logic against interfaces. Integration-test controllers with `envtest`. E2E-test full scenarios in a real cluster. Avoid testing implementation details — a refactor should not break a test unless observable behavior changed.

8. Instrument everything that matters to an operator
Expose Prometheus metrics for reconcile latency, error rates, queue depth, and sync counts. Emit structured events on the objects you manage. Add controller-runtime health/ready probes. Observability is not optional in a cluster — it is the interface.

9. Maintain a living CONTRIBUTING guide and clear ADRs
Document *why* architectural decisions were made, not just what they are. A contributor who understands the reasoning makes better PRs. Keep `CONTRIBUTING.md` updated on every workflow change. Stale docs erode trust faster than missing docs.

10. Lint, vet, and enforce style in CI — not in review
Run `golangci-lint`, `go vet`, `staticcheck`, and `controller-gen` in every PR pipeline. Enforce `gofmt`. Use a strict but documented linter config committed to the repo. Move style debates out of code review and into the config file where they belong.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

<!-- end:coding-guardrails -->
