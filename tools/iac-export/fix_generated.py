#!/usr/bin/env python3
"""Clean up `tofu/terraform plan -generate-config-out` output so it validates.

Generated config echoes every attribute the API returned, including empty
values the provider treats as conflicting or invalid. These rules are generic
(no organisation-specific names):

- drop attributes set to [] / "" / {}  (empty values conflict with their siblings), except where
  the empty value is meaningful (Pub/Sub `ttl = ""` = never expire), then drop nested blocks left empty
- drop enum placeholders such as *_UNSPECIFIED
- cloudbuild triggers: a `filename` trigger must not also carry an inline `build` block
- BigQuery datasets: drop default_table_expiration_ms below the 1 h minimum (0 means "never")

- ownership: Cloud Run services/jobs are rolled out by each app's own CI, so Terraform ignores their
  revision template (image, env, scaling) and owns the service, ingress, IAM and domain mappings
- known provider/API normalisation quirks are ignored per type (see IGNORE, each with a reason)
- secrets: env values that look like credentials are replaced by a placeholder, and the run FAILS
  if anything resembling key material is left (never commit generated secrets)

    python3 fix_generated.py generated.tf      # rewrites in place, prints a summary
"""
import re
import sys
from collections import Counter

EMPTY = re.compile(r'^\s+[A-Za-z0-9_]+\s*=\s*(\[\]|""|\{\})\s*$')
UNSPEC = re.compile(r'^\s+[A-Za-z0-9_]+\s*=\s*"[A-Z_]*_UNSPECIFIED"\s*$')
KEEP_EMPTY = {"ttl", "default_kms_key_name"}

# resource type -> (ignore_changes attributes, reason). Printed into a lifecycle block.
IGNORE = {
    "google_cloud_run_v2_service": (["client", "client_version", "template", "scaling", "build_config", "traffic"], "revisions and traffic are rolled out by the app's own CI"),
    "google_cloud_run_v2_job": (["client", "client_version", "template"], "job revisions are deployed by the app's own CI"),
    "google_cloud_run_domain_mapping": (["spec[0].certificate_mode"], "API returns it empty; unset would default to AUTOMATIC and force a replace"),
    "google_artifact_registry_repository": (["cleanup_policies"], "provider rejects the TAG_STATE_UNSPECIFIED the API returns"),
    "google_cloudbuild_trigger": (["build"], "inline build defaults (timeout) are not returned by the API"),
    "google_monitoring_alert_policy": (["documentation"], "API does not return the default mime_type"),
}
SET_ATTR = {
    # never switch an API off because a config line was deleted
    "google_project_service": ("disable_on_destroy", "false"),
}
DROP_LINE = {}
SECRET_NAME = re.compile(r"(KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL|PRIVATE)", re.I)
SECRET_VALUE = re.compile(
    r'(BEGIN [A-Z ]*PRIVATE KEY|private_key|\bsk-[A-Za-z0-9_-]{16,}|AIza[0-9A-Za-z_-]{30,}'
    r'|gh[pousr]_[A-Za-z0-9]{30,}|xox[abpr]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}'
    r'|\b[a-z][a-z0-9+.-]*://[^:@/\s"]+:[^@\s"]+@'  # any URL carrying user:password@
    r'|"[A-Za-z0-9+/=]{200,}")'  # long base64 blobs (scripts or keys that may embed credentials)
)
REDACTED = '"REDACTED by iac-export: set by the app deploy; move this value to Secret Manager"'
BQ_EXP = re.compile(r'^\s+default_(table|partition)_expiration_ms\s*=\s*(\d+)\s*$')


def split_resources(text):
    """Yield top-level blocks (resource "t" "n" { ... }) and the text between them."""
    out, cur, depth = [], [], 0
    for line in text.splitlines(keepends=True):
        cur.append(line)
        depth += line.count("{") - line.count("}")
        if depth == 0 and line.strip() == "}":
            out.append("".join(cur))
            cur = []
    if cur:
        out.append("".join(cur))
    return out


def drop_block(lines, name):
    """Remove a nested block `name {` … matching `}` (first level only)."""
    out, skip, depth = [], False, 0
    for ln in lines:
        if not skip and re.match(rf"^\s{{2}}{name}\s*\{{\s*$", ln):
            skip, depth = True, 0
        if skip:
            depth += ln.count("{") - ln.count("}")
            if depth == 0:
                skip = False
            continue
        out.append(ln)
    return out


def keep_original_empty_blocks(lines):
    """Blocks that are empty in the API's own answer (e.g. secret `replication { auto {} }`) are meaningful:
    fold them to one line `name {}` so the emptied-block pass below never removes them."""
    out, i = [], 0
    while i < len(lines):
        m = re.match(r"^(\s{2,})([A-Za-z0-9_]+)\s*\{\s*$", lines[i])
        if m and i + 1 < len(lines) and lines[i + 1].strip() == "}":
            out.append(f"{m.group(1)}{m.group(2)} {{}}\n")
            i += 2
            continue
        out.append(lines[i])
        i += 1
    return out


def drop_empty_blocks(lines):
    """Remove nested `name {` / `}` pairs with nothing between them (repeat until stable)."""
    total = 0
    while True:
        out, i, n = [], 0, 0
        while i < len(lines):
            if i + 1 < len(lines) and re.match(r"^\s{2,}[A-Za-z0-9_]+\s*\{\s*$", lines[i]) and lines[i + 1].strip() == "}":
                i, n = i + 2, n + 1
                continue
            out.append(lines[i])
            i += 1
        total += n
        lines = out
        if n == 0:
            return lines, total


def redact_env(lines):
    """Replace credential-looking env values (single- or multi-line) with a placeholder."""
    out, i, n = [], 0, 0
    while i < len(lines):
        ln = lines[i]
        if re.match(r"^\s+env\s*\{\s*$", ln):
            depth, j = 0, i
            while j < len(lines):
                depth += lines[j].count("{") - lines[j].count("}")
                if depth == 0:
                    break
                j += 1
            block = lines[i:j + 1]
            text = "".join(block)
            name = re.search(r'name\s*=\s*"([^"]*)"', text)
            if SECRET_VALUE.search(text) or (name and SECRET_NAME.search(name.group(1)) and re.search(r"\bvalue\s*=", text)):
                indent = re.match(r"^(\s+)", ln).group(1)
                out.append(f"{indent}env {{\n{indent}  name  = \"{name.group(1) if name else ''}\"\n{indent}  value = {REDACTED}\n{indent}}}\n")
                n += 1
                i = j + 1
                continue
        out.append(ln)
        i += 1
    return out, n


def main(path):
    text = open(path).read()
    stats = Counter()
    blocks = []
    for blk in split_resources(text):
        lines = keep_original_empty_blocks(blk.splitlines(keepends=True))
        head = next((l for l in lines if l.startswith("resource ")), "")
        kept, in_map = [], 0
        for ln in lines:
            if in_map:
                in_map += ln.count("{") - ln.count("}")
                kept.append(ln)
                continue
            if re.match(r"^\s+[A-Za-z0-9_]+\s*=\s*\{\s*$", ln):
                in_map = 1
                kept.append(ln)
                continue
            if EMPTY.match(ln) and ln.split("=")[0].strip() not in KEEP_EMPTY:
                stats["empty value"] += 1
                continue
            if UNSPEC.match(ln):
                stats["*_UNSPECIFIED"] += 1
                continue
            m = BQ_EXP.match(ln)
            if m and int(m.group(2)) < 3600000:
                stats["bigquery expiration < 1h"] += 1
                continue
            kept.append(ln)
        if head.startswith('resource "google_cloudbuild_trigger"') and any(re.match(r"^\s{2}filename\s*=", l) for l in kept):
            before = len(kept)
            kept = drop_block(kept, "build")
            if len(kept) != before:
                stats["cloudbuild inline build (filename trigger)"] += 1
        rtype = head.split('"')[1] if head.startswith("resource ") else ""
        if rtype in DROP_LINE:
            kept = [l for l in kept if not DROP_LINE[rtype].match(l)]
        if rtype in ("google_cloud_run_v2_service", "google_cloud_run_v2_job"):
            kept, n = redact_env(kept)
            stats["redacted env values"] += n
        if rtype == "google_cloudbuild_trigger" and not any(re.match(r"^\s{4}timeout\s*=", l) for l in kept):
            kept = [l + '    timeout = "600s" # API default, not returned\n' if re.match(r"^\s{2}build\s*\{\s*$", l) else l for l in kept]
        if rtype in SET_ATTR:
            key, val = SET_ATTR[rtype]
            rx = re.compile(rf"^(\s+){key}(\s*)=.*$")
            kept = [rx.sub(rf"\g<1>{key}\g<2>= {val}", l.rstrip("\n")) + "\n" if rx.match(l) else l for l in kept]
        if rtype in IGNORE and kept and kept[-1].strip() == "}":
            attrs, why = IGNORE[rtype]
            kept[-1:] = [f"  lifecycle {{\n    # {why}\n    ignore_changes = [{', '.join(attrs)}]\n  }}\n", "}\n"]
            stats[f"lifecycle {rtype}"] += 1
        kept, n = drop_empty_blocks(kept)
        stats["emptied nested block"] += n
        blocks.append("".join(kept))
    result = "".join(blocks)
    leaks = [m.group(0)[:30] for m in SECRET_VALUE.finditer(result)]
    open(path, "w").write(result)
    print(dict(stats))
    if leaks:
        print(f"FAIL: {len(leaks)} credential-looking value(s) remain, e.g. {leaks[:3]}. Do not commit; extend the redaction.", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main(sys.argv[1])
