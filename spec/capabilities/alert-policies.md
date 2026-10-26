# Capability: Alert policies, templates, uptime checks, channels, snoozes (Alerts feature, real)

## What It Does
Manages Cloud Monitoring alerting from the dashboard: 13 templates (one policy per template per project, written to cover every resource of the type incl. future ones), uptime checks, notification channels with severity routing, snoozes, coverage analysis, adoption of the four existing unmanaged policies. Every change is Preview → Apply.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Projects | `CLOUD0_GCP_PROJECTS` | env | yes |
| Reader credentials | existing `cloud0-reader` | env | reads |
| Alerts credentials | `CLOUD0_ALERTS_CREDENTIALS` (`alertPolicyEditor`, `notificationChannelEditor`, `uptimeCheckConfigEditor`, `snoozeEditor`, viewer) | env | apply |
| Template params, uptime drafts, snooze drafts, routing | `spec/api.md → Alerts additions` | user | edits |

## Outputs
| Output | Type | Destination |
|---|---|---|
| policies, channels, templates, uptime-checks, snoozes, coverage, routing, adopt preview | GET/PUT/POST under `/api/v1/alerts/*` | UI Alerts tab |
| preview / apply | `Preview` (policy JSON + gcloud + diff) / `ApplyResult` | UI, `change_history` |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Monitoring v3 `AlertPolicyService`, `NotificationChannelService` (+descriptors), `UptimeCheckService`, `SnoozeService` | list/get/create/update/delete | envelope per `spec/api.md` (`permission_denied` with role hint, `upstream_error`, `api_disabled`) |

## Business Rules
- Package `internal/alerting` is the contract: `Builder` is pure; `PolicyClient`/`ChannelClient`/`UptimeClient`/`SnoozeClient` are the only GCP code; the writing client uses ONLY alerts credentials, reader code is unchanged.
- No apply without a preview token (10 min TTL; hash of target + desired + live state); changed live state → 409 `preview_stale`; token single use. Deletes need `confirm_delete`.
- Managed = labels `managed-by=cloud0`, `cloud0-template=<id>` (+ `cloud0-instance`). Templates never touch unmanaged resources; unmanaged ones change only by an explicit target.
- Adopt & fix: create the managed replacement (preview → apply), then offer to DISABLE the old policy (separate preview/apply); never delete automatically. Shapes: Zero-Backend CPU Spike → `vm_cpu_high`; High memory load (85 %, actually duration 0 s) → `vm_memory_high` 15 min; the two uptime policies → `uptime_down` warning 10 min + critical 2 h.
- `run_error_logged` is a log-match alert titled "Application error logged" (no Monitoring condition for "new error group"); 4xx never alerts by default; `run_401_403_spike` is created disabled.
- Memory/disk use Ops Agent metrics and skip VMs without the agent: coverage surfaces "install the Ops Agent". Cloud SQL memory on shared-core tiers is skipped with a hint.
- Email channel verification is checked and warned about; webhook/Slack secrets are passed through to GCP only, never stored or logged. Slack needing console OAuth is stated in the UI; the user picks an existing channel.
- Severity → channels routing lives in settings `alerts.routing.<severity>`; default = the channel named "Alert".
- Real-GCP write tests use display-name prefix `cloud0-test-`, create DISABLED policies with NO channels, and delete everything in `t.Cleanup`, failing loudly if deletion fails; they never touch existing resources.

## Success Criteria
- [ ] Real read: list returns the 4 unmanaged policies (managed=false, adopt_shape set), ≥ 1 channel (email, warning if unverified), 1 uptime check.
- [ ] Every template builds valid JSON that GCP accepts (create disabled `cloud0-test-` policy → read back → delete).
- [ ] Preview never changes GCP; apply with stale token → 409; every apply writes exactly one history row.
- [ ] Coverage flags a VM without the Ops Agent for memory/disk.
- [ ] e2e: Alerts tab lists policies with managed/unmanaged state, template form → preview shows JSON + gcloud + diff.
