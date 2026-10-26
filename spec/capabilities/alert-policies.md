# Capability: Alert policies & channels (Phase 4)

## What It Does
Lists, creates and edits Cloud Monitoring alert policies and lists notification channels, so the team manages alerting from the same dashboard.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Projects | `CLOUD0_GCP_PROJECTS` | env | yes |
| Policy definition | display name, condition (metric filter, comparison, threshold, duration), channels, enabled | user | for create/edit |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Policies / channels | `GET /api/v1/alerts/policies`, `POST`, `PUT /api/v1/alerts/policies/{id}`, `GET /api/v1/alerts/channels` | UI |
| Change history | `change-history.md` | store |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Cloud Monitoring (`monitoring/apiv3/v2` AlertPolicyClient, NotificationChannelClient) | list/create/update | envelope with hint (`roles/monitoring.alertPolicyEditor`, `roles/monitoring.notificationChannelViewer`) |

## Business Rules
- Read-only works with `roles/monitoring.viewer` (already granted); writes need the editor role, requested in the README setup step.
- Templates offered: VM CPU spike, uptime failure, high memory; the exact condition JSON is shown before saving.
- Every write recorded in `change_history` with the before/after JSON.

## Success Criteria
- [ ] Real API: list returns the existing policies (≥ 4 in the known environment) and ≥ 1 channel.
- [ ] Create a disabled test policy → appears → update display name → delete → history rows written.
- [ ] e2e: the tab lists policies with enabled state and channel names.
