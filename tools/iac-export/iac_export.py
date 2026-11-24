#!/usr/bin/env python3
"""Export a live GCP project to Terraform import blocks.

Reads Cloud Asset Inventory (and IAM policies) through the gcloud CLI and writes one
`imports_<kind>.tf` file per resource kind. Then

    tofu plan -generate-config-out=generated.tf      # or terraform plan …

lets Terraform write the resource blocks that match the live state, so the
first plan is "N to import, 0 to add/change/destroy".

Generic: nothing here is specific to one organisation. Standard library only.

    python3 iac_export.py --project <PROJECT_ID> --out <DIR> [--exclude-ids FILE]

Skipped on purpose (state churn, Google-managed, or secret material):
revisions/executions/images, data tables, keys and secret versions, backups and
snapshots, auto-created default subnets and serverless egress addresses,
system log sinks/buckets, API keys (their key string would enter state).
"""
import argparse
import json
import re
import subprocess
import sys
from collections import defaultdict


def gcloud(*args):
    out = subprocess.run(["gcloud", *args, "--format=json"], capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"gcloud {' '.join(args)}: {out.stderr.strip()[:300]}")
    return json.loads(out.stdout or "[]")


def tf_name(*parts):
    n = "_".join(p for p in parts if p)
    n = re.sub(r"[^a-zA-Z0-9_]", "_", n).strip("_").lower()
    n = re.sub(r"_+", "_", n)
    if not n or n[0].isdigit():
        n = "r_" + n
    return n[:120]


def seg(path, key):
    """Value after `key/` in a resource path."""
    parts = path.split("/")
    for i, p in enumerate(parts[:-1]):
        if p == key:
            return parts[i + 1]
    return ""


# asset type -> (terraform type, import-id builder(path, project), name builder(path))
def _last(p):
    return p.rsplit("/", 1)[-1]


MAP = {
    "serviceusage.googleapis.com/Service": ("google_project_service", lambda p, pr: f"{pr}/{_last(p)}", lambda p: _last(p)),
    "iam.googleapis.com/ServiceAccount": ("google_service_account", lambda p, pr: f"projects/{pr}/serviceAccounts/{_last(p)}", lambda p: _last(p).split("@")[0]),
    "iam.googleapis.com/Role": ("google_project_iam_custom_role", lambda p, pr: f"projects/{pr}/roles/{_last(p)}", _last),
    "iam.googleapis.com/WorkloadIdentityPool": ("google_iam_workload_identity_pool", lambda p, pr: f"projects/{pr}/locations/global/workloadIdentityPools/{seg(p, 'workloadIdentityPools')}", lambda p: seg(p, "workloadIdentityPools")),
    "iam.googleapis.com/WorkloadIdentityPoolProvider": ("google_iam_workload_identity_pool_provider", lambda p, pr: f"projects/{pr}/locations/global/workloadIdentityPools/{seg(p, 'workloadIdentityPools')}/providers/{_last(p)}", lambda p: seg(p, "workloadIdentityPools") + "_" + _last(p)),
    "secretmanager.googleapis.com/Secret": ("google_secret_manager_secret", lambda p, pr: f"projects/{pr}/secrets/{_last(p)}", _last),
    "run.googleapis.com/Service": ("google_cloud_run_v2_service", lambda p, pr: f"projects/{pr}/locations/{seg(p, 'locations')}/services/{_last(p)}", _last),
    "run.googleapis.com/Job": ("google_cloud_run_v2_job", lambda p, pr: f"projects/{pr}/locations/{seg(p, 'locations')}/jobs/{_last(p)}", _last),
    "run.googleapis.com/DomainMapping": ("google_cloud_run_domain_mapping", lambda p, pr: f"locations/{seg(p, 'locations')}/namespaces/{pr}/domainmappings/{_last(p)}", _last),
    "artifactregistry.googleapis.com/Repository": ("google_artifact_registry_repository", lambda p, pr: f"projects/{pr}/locations/{seg(p, 'locations')}/repositories/{_last(p)}", _last),
    "storage.googleapis.com/Bucket": ("google_storage_bucket", lambda p, pr: _last(p), _last),
    "compute.googleapis.com/Network": ("google_compute_network", lambda p, pr: f"projects/{pr}/global/networks/{_last(p)}", _last),
    "compute.googleapis.com/Subnetwork": ("google_compute_subnetwork", lambda p, pr: f"projects/{pr}/regions/{seg(p, 'regions')}/subnetworks/{_last(p)}", lambda p: seg(p, "regions") + "_" + _last(p)),
    "compute.googleapis.com/Firewall": ("google_compute_firewall", lambda p, pr: f"projects/{pr}/global/firewalls/{_last(p)}", _last),
    "compute.googleapis.com/Instance": ("google_compute_instance", lambda p, pr: f"projects/{pr}/zones/{seg(p, 'zones')}/instances/{_last(p)}", _last),
    "compute.googleapis.com/Disk": ("google_compute_disk", lambda p, pr: f"projects/{pr}/zones/{seg(p, 'zones')}/disks/{_last(p)}", _last),
    "compute.googleapis.com/HealthCheck": ("google_compute_health_check", lambda p, pr: f"projects/{pr}/global/healthChecks/{_last(p)}", _last),
    "compute.googleapis.com/SslCertificate": ("google_compute_managed_ssl_certificate", lambda p, pr: f"projects/{pr}/global/sslCertificates/{_last(p)}", _last),
    "compute.googleapis.com/UrlMap": ("google_compute_url_map", lambda p, pr: f"projects/{pr}/global/urlMaps/{_last(p)}", _last),
    "compute.googleapis.com/TargetHttpsProxy": ("google_compute_target_https_proxy", lambda p, pr: f"projects/{pr}/global/targetHttpsProxies/{_last(p)}", _last),
    "compute.googleapis.com/TargetHttpProxy": ("google_compute_target_http_proxy", lambda p, pr: f"projects/{pr}/global/targetHttpProxies/{_last(p)}", _last),
    "compute.googleapis.com/InstanceGroup": ("google_compute_instance_group", lambda p, pr: f"projects/{pr}/zones/{seg(p, 'zones')}/instanceGroups/{_last(p)}", _last),
    "compute.googleapis.com/ResourcePolicy": ("google_compute_resource_policy", lambda p, pr: f"projects/{pr}/regions/{seg(p, 'regions')}/resourcePolicies/{_last(p)}", lambda p: seg(p, "regions") + "_" + _last(p)),
    "cloudscheduler.googleapis.com/Job": ("google_cloud_scheduler_job", lambda p, pr: f"projects/{pr}/locations/{seg(p, 'locations')}/jobs/{_last(p)}", _last),
    "pubsub.googleapis.com/Topic": ("google_pubsub_topic", lambda p, pr: f"projects/{pr}/topics/{_last(p)}", _last),
    "pubsub.googleapis.com/Subscription": ("google_pubsub_subscription", lambda p, pr: f"projects/{pr}/subscriptions/{_last(p)}", _last),
    "eventarc.googleapis.com/Trigger": ("google_eventarc_trigger", lambda p, pr: f"projects/{pr}/locations/{seg(p, 'locations')}/triggers/{_last(p)}", _last),
    "logging.googleapis.com/LogSink": ("google_logging_project_sink", lambda p, pr: f"projects/{pr}/sinks/{_last(p)}", _last),
    "monitoring.googleapis.com/AlertPolicy": ("google_monitoring_alert_policy", lambda p, pr: f"projects/{pr}/alertPolicies/{_last(p)}", None),
    "monitoring.googleapis.com/Dashboard": ("google_monitoring_dashboard", lambda p, pr: f"projects/{pr}/dashboards/{_last(p)}", None),
    "bigquery.googleapis.com/Dataset": ("google_bigquery_dataset", lambda p, pr: f"projects/{pr}/datasets/{_last(p)}", _last),
    "sqladmin.googleapis.com/Instance": ("google_sql_database_instance", lambda p, pr: f"projects/{pr}/instances/{_last(p)}", _last),
    "monitoring.googleapis.com/NotificationChannel": ("google_monitoring_notification_channel", lambda p, pr: f"projects/{pr}/notificationChannels/{_last(p)}", None),
    "monitoring.googleapis.com/UptimeCheckConfig": ("google_monitoring_uptime_check_config", lambda p, pr: f"projects/{pr}/uptimeCheckConfigs/{_last(p)}", _last),
    "cloudbuild.googleapis.com/BuildTrigger": ("google_cloudbuild_trigger", lambda p, pr: f"projects/{pr}/locations/{seg(p, 'locations')}/triggers/{_last(p)}", None),
    "storagetransfer.googleapis.com/TransferJob": ("google_storage_transfer_job", lambda p, pr: f"{pr}/{_last(p)}", None),
    "servicenetworking.googleapis.com/Connection": ("google_service_networking_connection", lambda p, pr: f"projects/{pr}/global/networks/{_last(p)}:servicenetworking.googleapis.com", lambda p: _last(p) + "_servicenetworking"),
    "orgpolicy.googleapis.com/Policy": ("google_org_policy_policy", lambda p, pr: f"projects/{pr}/policies/{_last(p)}", _last),
    "osconfig.googleapis.com/OSPolicyAssignment": ("google_os_config_os_policy_assignment", lambda p, pr: f"projects/{pr}/locations/{seg(p, 'locations')}/osPolicyAssignments/{_last(p)}", _last),
}

# Regional/global variants that share an asset type.
def refine(atype, tftype, path, pr):
    if atype == "compute.googleapis.com/Address":
        if "/regions/" in path:
            return "google_compute_address", f"projects/{pr}/regions/{seg(path, 'regions')}/addresses/{_last(path)}"
        return "google_compute_global_address", f"projects/{pr}/global/addresses/{_last(path)}"
    if atype == "compute.googleapis.com/BackendService":
        if "/regions/" in path:
            return "google_compute_region_backend_service", f"projects/{pr}/regions/{seg(path, 'regions')}/backendServices/{_last(path)}"
        return "google_compute_backend_service", f"projects/{pr}/global/backendServices/{_last(path)}"
    if atype == "compute.googleapis.com/ForwardingRule":
        if "/regions/" in path:
            return "google_compute_forwarding_rule", f"projects/{pr}/regions/{seg(path, 'regions')}/forwardingRules/{_last(path)}"
        return "google_compute_global_forwarding_rule", f"projects/{pr}/global/forwardingRules/{_last(path)}"
    if atype == "compute.googleapis.com/NetworkEndpointGroup":
        if "/regions/" in path:
            return "google_compute_region_network_endpoint_group", f"projects/{pr}/regions/{seg(path, 'regions')}/networkEndpointGroups/{_last(path)}"
        return "google_compute_network_endpoint_group", f"projects/{pr}/zones/{seg(path, 'zones')}/networkEndpointGroups/{_last(path)}"
    return None


SKIP_NAME = [
    (re.compile(r"/subnetworks/default$"), "auto-mode default subnet (owned by the default network)"),
    (re.compile(r"/addresses/serverless-ipv4-"), "Cloud Run egress address (managed by Cloud Run)"),
    (re.compile(r"/sinks/_(Default|Required)$"), "system log sink"),
    # Google-created default service accounts cannot be managed as google_service_account
    # (their IAM members are still exported).
    (re.compile(r"/serviceAccounts/(\d+-compute@developer|[^/]+@appspot)\.gserviceaccount\.com$"), "default service account"),
]

# Resource kinds whose IAM members are exported (user permissions as code).
IAM_KINDS = {
    "google_cloud_run_v2_service": ("google_cloud_run_v2_service_iam_member", lambda r: ["run", "services", "get-iam-policy", r["name"], f"--region={r['loc']}", f"--project={r['project']}"], lambda r, role, m: f"projects/{r['project']}/locations/{r['loc']}/services/{r['name']} {role} {m}"),
    "google_storage_bucket": ("google_storage_bucket_iam_member", lambda r: ["storage", "buckets", "get-iam-policy", f"gs://{r['name']}"], lambda r, role, m: f"b/{r['name']} {role} {m}"),
    "google_secret_manager_secret": ("google_secret_manager_secret_iam_member", lambda r: ["secrets", "get-iam-policy", r["name"], f"--project={r['project']}"], lambda r, role, m: f"projects/{r['project']}/secrets/{r['name']} {role} {m}"),
    "google_service_account": ("google_service_account_iam_member", lambda r: ["iam", "service-accounts", "get-iam-policy", r["email"], f"--project={r['project']}"], lambda r, role, m: f"projects/{r['project']}/serviceAccounts/{r['email']} {role} {m}"),
}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--project", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--exclude-ids", help="file of import ids (one per line) already managed elsewhere")
    ap.add_argument("--no-iam", action="store_true", help="skip IAM members")
    args = ap.parse_args()
    pr = args.project
    number = gcloud("projects", "describe", pr)["projectNumber"]
    exclude = set()
    if args.exclude_ids:
        exclude = {l.strip() for l in open(args.exclude_ids) if l.strip() and not l.startswith("#")}

    assets = gcloud("asset", "search-all-resources", f"--scope=projects/{pr}")
    by_kind = defaultdict(list)  # tf type -> [(tfname, id)]
    skipped = defaultdict(int)
    iam_targets = defaultdict(list)
    names_seen = defaultdict(set)

    for a in assets:
        atype = a["assetType"]
        path = a["name"].split(".googleapis.com/", 1)[1].replace(f"projects/{number}/", f"projects/{pr}/")
        if any(rx.search(path) for rx, _ in SKIP_NAME):
            skipped["auto/system"] += 1
            continue
        ref = refine(atype, None, path, pr)
        if ref:
            tftype, iid = ref
            name = tf_name(_last(path))
        elif atype in MAP:
            tftype, idf, nf = MAP[atype]
            iid = idf(path, pr)
            disp = a.get("displayName") or _last(path)
            name = tf_name(nf(path) if nf else disp)
        else:
            skipped[atype.split("/")[-1]] += 1
            continue
        if iid in exclude:
            skipped["managed elsewhere"] += 1
            continue
        base, i = name, 2
        while name in names_seen[tftype]:
            name, i = f"{base}_{i}", i + 1
        names_seen[tftype].add(name)
        by_kind[tftype].append((name, iid))
        if tftype in IAM_KINDS:
            r = {"project": pr, "name": _last(path), "loc": seg(path, "locations")}
            if tftype == "google_service_account":
                r["email"] = _last(path)
            iam_targets[tftype].append((name, r))

    # Cloud SQL databases and users are not searchable assets: list them per instance.
    for name, iid in list(by_kind.get("google_sql_database_instance", [])):
        inst = iid.rsplit("/", 1)[-1]
        for db in gcloud("sql", "databases", "list", f"--instance={inst}", f"--project={pr}"):
            if db["name"] in ("postgres", "mysql", "information_schema", "sys", "performance_schema"):
                continue
            by_kind["google_sql_database"].append((tf_name(inst, db["name"]), f"projects/{pr}/instances/{inst}/databases/{db['name']}"))
        for u in gcloud("sql", "users", "list", f"--instance={inst}", f"--project={pr}"):
            if u["name"] in ("postgres", "root", "mysql.sys", "cloudsqlsuperuser"):
                continue
            host = u.get("host", "")
            by_kind["google_sql_user"].append((tf_name(inst, u["name"]), f"{pr}/{inst}/{host}/{u['name']}" if host else f"{pr}/{inst}/{u['name']}"))

    # Project IAM: one member resource per (role, member). Conditional bindings are kept out (reported).
    if not args.no_iam:
        pol = gcloud("projects", "get-iam-policy", pr)
        for b in pol.get("bindings", []):
            if b.get("condition"):
                skipped["conditional project binding"] += 1
                continue
            for m in b["members"]:
                iid = f"{pr} {b['role']} {m}"
                if iid in exclude or m.startswith("deleted:"):
                    skipped["managed elsewhere" if iid in exclude else "deleted member"] += 1
                    continue
                by_kind["google_project_iam_member"].append((tf_name(b["role"].split("/")[-1], m.split(":")[-1].split("@")[0]), iid))
        for kind, targets in iam_targets.items():
            itype, cmd, idf = IAM_KINDS[kind]
            for parent, r in targets:
                try:
                    pol = gcloud(*cmd(r))
                except RuntimeError as e:
                    print(f"warn: {kind} {r['name']}: {e}", file=sys.stderr)
                    continue
                for b in pol.get("bindings", []):
                    if b.get("condition"):
                        skipped["conditional binding"] += 1
                        continue
                    for m in b["members"]:
                        iid = idf(r, b["role"], m)
                        if iid in exclude or m.startswith("deleted:"):
                            continue
                        by_kind[itype].append((tf_name(parent, b["role"].split("/")[-1], m.split(":")[-1].split("@")[0]), iid))

    total = 0
    for tftype, items in sorted(by_kind.items()):
        seen = set()
        with open(f"{args.out}/imports_{tftype.removeprefix('google_')}.tf", "w") as f:
            f.write(f"# Generated by cloud0 tools/iac-export from project inventory. {len(items)} resources.\n\n")
            for name, iid in items:
                n, i = name, 2
                while n in seen:
                    n, i = f"{name}_{i}", i + 1
                seen.add(n)
                f.write(f'import {{\n  to = {tftype}.{n}\n  id = "{iid}"\n}}\n\n')
                total += 1
    print(f"wrote {total} import blocks in {len(by_kind)} kinds to {args.out}")
    for k, v in sorted(by_kind.items(), key=lambda kv: -len(kv[1])):
        print(f"  {len(v):4d}  {k}")
    print("skipped:", dict(sorted(skipped.items(), key=lambda kv: -kv[1])))


if __name__ == "__main__":
    main()
