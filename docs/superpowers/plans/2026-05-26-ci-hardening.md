# CI Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make GitHub Actions fail faster, run with narrower permissions, cancel stale PR runs, and catch Go data races.

**Architecture:** Keep the existing three-workflow split. Add shared hardening fields directly to each workflow and add a dedicated Go race-test step after the existing `make all` gate.

**Tech Stack:** GitHub Actions YAML, Go test race detector, existing Go and Elixir Makefiles.

---

### Task 1: Harden Workflow Metadata

**Files:**
- Modify: `.github/workflows/go.yml`
- Modify: `.github/workflows/make-all.yml`
- Modify: `.github/workflows/pr-description-lint.yml`

- [ ] **Step 1: Add workflow-level permissions and concurrency**

Add this shape to each workflow after `on`:

```yaml
permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true
```

- [ ] **Step 2: Add job timeouts**

Use `timeout-minutes: 10` for Go and PR description lint. Use `timeout-minutes: 20` for Elixir `make-all`, because it runs setup, coverage, and dialyzer.

- [ ] **Step 3: Validate YAML**

Run:

```sh
git diff --check
```

Expected: exit 0.

### Task 2: Add Go Race Gate

**Files:**
- Modify: `.github/workflows/go.yml`

- [ ] **Step 1: Add race-test step**

Add this after `Verify Go implementation`:

```yaml
- name: Verify Go race safety
  run: go test -race ./...
```

- [ ] **Step 2: Run local Go verification**

Run:

```sh
make -C go all
```

Expected: exit 0.

### Task 3: Reduce Existing CI Flake

**Files:**
- Modify: `elixir/test/symphony_elixir/ssh_test.exs`

- [ ] **Step 1: Increase fake SSH trace wait budget**

The full Elixir coverage run can start the fake ssh port slower than the current 500ms trace wait.
Increase the helper default from 20 attempts to 100 attempts while keeping the 25ms polling interval.

- [ ] **Step 2: Verify the focused SSH test**

Run:

```sh
cd elixir && mise exec -- mix test test/symphony_elixir/ssh_test.exs:135
```

Expected: exit 0.

- [ ] **Step 3: Run local Elixir verification**

Run:

```sh
make -C elixir MIX='mise exec -- mix' all
```

Expected: exit 0.
