#!/usr/bin/env python3
"""Collect the open issues that track compat results, for the public report.

An issue says which results it tracks with a hidden marker in its body:

    <!-- compat:sqs/PurgeQueue -->
    <!-- compat:iam/iam-gen-role/PutRolePermissionsBoundary@rust-sdk -->
    <!-- compat:cognito-idp -->

A target is <service>[/<group-or-operation>[/<test>]][@<suite>]. One marker may
list several targets separated by commas, and an issue may carry any number of
markers. `cmd/compat --publish-report --issues-file` links each result to the
most specific target that matches it; compat/report-issues.json overrides both.

Only open issues labelled `compat` are read, so closing the issue is how a link
is retired.

Usage: python3 scripts/compat-issues.py [out.json]   (default compat-issues.json)

Environment: GH_TOKEN (required by gh), GH_REPO (optional, defaults to the
current repository).
"""

from __future__ import annotations

import json
import re
import subprocess
import sys

LABEL = "compat"
MARKER = re.compile(r"<!--\s*compat:([^>]*?)\s*-->")
# A flake-issue marker (<!-- compat-flake:... -->) does not match: the colon
# has to follow "compat" directly. Those issues are linked through flaky.json.
TARGET = re.compile(r"^[a-z0-9][a-z0-9-]*(/[A-Za-z0-9_.-]+){0,2}(@[a-z0-9-]+)?$")


def targets_in(body: str) -> list[str]:
    out: list[str] = []
    for match in MARKER.finditer(body or ""):
        for raw in match.group(1).split(","):
            target = raw.strip()
            if not target:
                continue
            if not TARGET.match(target):
                print(f"compat-issues: ignoring malformed target {target!r}", file=sys.stderr)
                continue
            if target not in out:
                out.append(target)
    return out


def main() -> int:
    out_path = sys.argv[1] if len(sys.argv) > 1 else "compat-issues.json"
    result = subprocess.run(
        ["gh", "issue", "list", "--state", "open", "--label", LABEL, "--limit", "2000",
         "--json", "number,title,url,state,body"],
        capture_output=True, text=True, encoding="utf-8",
    )
    if result.returncode != 0:
        print(f"compat-issues: gh issue list failed: {result.stderr.strip()}", file=sys.stderr)
        return 1
    issues = []
    for row in json.loads(result.stdout or "[]"):
        targets = targets_in(row.get("body", ""))
        if targets:
            issues.append({
                "number": row["number"], "url": row["url"], "title": row["title"],
                "state": row.get("state", "OPEN").lower(), "targets": targets,
            })
    issues.sort(key=lambda i: i["number"])
    with open(out_path, "w", encoding="utf-8", newline="\n") as f:
        json.dump({"version": 1, "issues": issues}, f, indent=2)
        f.write("\n")
    linked = sum(len(i["targets"]) for i in issues)
    print(f"compat-issues: {len(issues)} issue(s), {linked} target(s) -> {out_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
