# Build journal (harness-improvement log)

Run start: 2026-10-26 ~18:20 local (Darwin, Claude Code, zero-shot-harness 0.2.0).
Entries record friction with the harness or runtime only: symptom → what was done → durable lesson.

## Intake

- **Symptom:** every gcloud credential on the machine was expired; `gcloud` cannot prompt for re-auth in a non-interactive session, so the environment research the user asked for was blocked at first. **Did:** asked the user to run `gcloud auth login` in their own terminal, continued intake meanwhile, re-checked token validity cheaply before resuming research. **Lesson:** the intake should include a "cloud CLI auth is live" preflight the moment an idea mentions a cloud provider, and the skill should say that re-auth is a user-only step.
- **Symptom:** `gcloud alpha monitoring policies list` and `gcloud beta error-reporting ...` tried to install SDK components and hung on a y/N prompt. **Did:** used the GA `gcloud monitoring policies list` and the REST APIs via `curl` + access token instead. **Lesson:** never call `gcloud alpha|beta` in non-interactive runs; prefer GA commands or REST.
- **Symptom:** `bq show --format=json` for a table printed a non-JSON banner first in one invocation, breaking a JSON parse. **Did:** re-ran with `--schema --format=prettyjson`. **Lesson:** pipe `bq` output through a tolerant parser or use `--schema`.
- **Symptom:** a Monitoring aggregation grouped by `metric.labels.instance_name` returned series whose `metric` had no `labels` key for Ops Agent metrics. **Did:** noted that Ops Agent series carry the instance in `resource.labels`; parse both. **Lesson:** group-by fields differ between `compute.googleapis.com` and `agent.googleapis.com` metrics.
- **Observation:** the user answered intake with mid-turn free-text messages (not via the question tool) twice; the skill handled it but the question tool's single-select "frontend" answer was later overridden by text. **Lesson:** always re-read mid-turn messages before invoking the builder; treat the latest statement as binding.
- **Observation:** the target repo is public; the harness has no "public repo → identifiers via env only" rule. Added as a brief constraint. **Lesson:** intake technical round should ask repo visibility.

## Design / scaffold (first builder invocation)

- **Symptom:** the scaffold code-generator could not run a single Bash command: the macOS Data volume had ~235 MiB free (204 GiB of 228 GiB used) and the tool's output-capture file hit `ENOSPC`. **Did:** stopped the build before any scaffold file was written, committed the spec so the design work is durable, and returned a BLOCKED handoff with the largest reclaimable directories listed (sizes only; nothing deleted — freeing space is the user's call). **Lesson:** intake/preflight should check free disk (`df -h`) and require a few GB headroom before a build that pulls Go module graphs, node_modules and a Playwright browser.
- **Symptom:** `GO111MODULE=off` is persisted in `~/Library/Application Support/go/env`, so bare `go build`/`go test` inside a module fails on this machine. **Did (planned for the scaffold):** the Makefile exports `GO111MODULE=on` and `GOTOOLCHAIN=local`; README notes the prefix for direct `go test` invocations. **Lesson:** preflight should print `go env GO111MODULE GOTOOLCHAIN` when the stack is Go.
- **Symptom:** latest releases of `modernc.org/sqlite`, `pressly/goose/v3` and `cloud.google.com/go/bigquery` declare `go >= 1.26`, which the binding Go 1.23 toolchain refuses without an auto-download. **Did (planned):** pin the newest Go-1.23-compatible versions with `GOTOOLCHAIN=local`. **Lesson:** when the user binds an older toolchain, the spec should say "pin deps to the toolchain" and the generator should probe before `go get`.
