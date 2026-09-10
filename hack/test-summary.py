#!/usr/bin/env python3
"""Render a Markdown summary of a `go test -json` run.

Reads the newline-delimited event stream that `go test -json` writes (see
`go doc cmd/test2json`) and prints a Markdown report: a headline, a
per-package table with coverage, and the failing/skipped tests. CI appends it
to the job summary and keeps one sticky comment per pull request; the same
command works locally against a saved stream.

Two things the raw event stream gets wrong for this repo, and what is done
about them:

  * Ginkgo suites are ONE Go test (`TestControllers`) carrying every spec.
    Counted as-is, two envtest suites with hundreds of specs contribute 2 to
    the headline. So a package whose output carries Ginkgo's own
    "Ran N of M Specs" / "N Passed | N Failed | ..." summary is counted in
    specs -- the executed truth -- and its wrapper test is not counted.
  * Subtests are reported as separate tests alongside their parent. Only
    leaf tests are counted, so a table-driven parent does not inflate
    the total.

The stream tolerates non-JSON lines: `make test` echoes its recipe, and the
tool-install steps print progress, all on the same stdout. Anything that is
not a JSON object is skipped rather than being an error (`go test -json`
itself documents that non-JSON text can appear).
"""

import argparse
import json
import os
import re
import sys
from collections import defaultdict
from pathlib import Path

# Failure output is verbatim test output: a runaway suite could blow past
# GitHub's 65536-character comment limit and lose the whole comment, so the
# block is capped. The full stream is always uploaded as an artifact.
FAILURE_LINES_TOTAL = 200

ANSI_RE = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")

# Ginkgo's default reporter, e.g.
#   Ran 24 of 24 Specs in 31.416 seconds
#   SUCCESS! -- 24 Passed | 0 Failed | 0 Pending | 0 Skipped
# MULTILINE throughout: these lines sit in the middle of the package output,
# which is matched as one string.
GINKGO_RAN_RE = re.compile(
    r"^Ran (\d+) of (\d+) Specs in ([\d.]+) seconds", re.MULTILINE
)
GINKGO_TOTALS_RE = re.compile(
    r"--\s*(\d+) Passed \| (\d+) Failed \| (\d+) Pending \| (\d+) Skipped",
    re.MULTILINE,
)
# The "Summarizing N Failures:" block names each failing spec.
GINKGO_FAILURE_RE = re.compile(
    r"^\s*\[(FAIL|PANICKED!|TIMEDOUT|ABORTED|INTERRUPTED)\]\s+(.*\S)\s*$",
    re.MULTILINE,
)


def strip_ansi(text):
    return ANSI_RE.sub("", text)


def parse_stream(paths):
    """Fold the go test -json events into per-package state.

    Returns (packages, missing). A stream file that does not exist is
    reported, never raised: rendering the summary must not be able to turn a
    green run red.
    """
    packages = {}
    missing = []

    def pkg(name):
        return packages.setdefault(
            name,
            {
                "tests": {},  # test name -> "pass" | "fail" | "skip"
                "output": [],  # package-level + test output, in order
                "elapsed": None,
                "result": None,  # pass | fail | skip (skip = no test files)
                "ginkgo": None,
                "ginkgo_failures": [],
            },
        )

    for path in paths:
        if not os.path.isfile(path):
            missing.append(str(path))
            continue
        with open(path, encoding="utf-8", errors="replace") as handle:
            for line in handle:
                line = line.strip()
                if not line.startswith("{"):
                    continue
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not isinstance(event, dict):
                    continue
                action = event.get("Action")
                name = event.get("Package")
                if not name:
                    # A BuildEvent (go help buildjson): a package that failed
                    # to compile carries ImportPath, never Package. Dropping
                    # these would report a run that never built as green.
                    build_package = event.get("ImportPath")
                    if not build_package or action not in ("build-output", "build-fail"):
                        continue
                    # `pkg [pkg.test]` names the test binary of a package.
                    entry = pkg(build_package.split(" [", 1)[0])
                    if action == "build-output":
                        entry["output"].append(strip_ansi(event.get("Output", "")))
                    else:
                        entry["result"] = "fail"
                    continue
                entry = pkg(name)
                test = event.get("Test")
                if action == "output":
                    entry["output"].append(strip_ansi(event.get("Output", "")))
                elif action in ("pass", "fail", "skip"):
                    if test:
                        entry["tests"][test] = action
                    else:
                        entry["result"] = action
                        entry["elapsed"] = event.get("Elapsed")

    for entry in packages.values():
        text = "".join(entry["output"])
        ran = GINKGO_RAN_RE.search(text)
        totals = GINKGO_TOTALS_RE.search(text) if ran else None
        if ran and totals:
            passed, failed, pending, skipped = (int(g) for g in totals.groups())
            entry["ginkgo"] = {
                "passed": passed,
                "failed": failed,
                # Pending (focus/pending specs) and Skipped both mean
                # "declared but not executed" to a reader.
                "skipped": pending + skipped,
                "seconds": float(ran.group(3)),
            }
            entry["ginkgo_failures"] = [
                f"{kind}: {desc}"
                for kind, desc in GINKGO_FAILURE_RE.findall(text)
            ]
    return packages, missing


def leaf_tests(tests):
    """Drop parents of subtests: only the leaves are real results."""
    names = list(tests)
    parents = set()
    for name in names:
        for other in names:
            if other != name and other.startswith(name + "/"):
                parents.add(name)
                break
    return {n: s for n, s in tests.items() if n not in parents}


def counts_for(entry):
    """(passed, failed, skipped) for one package, specs where Ginkgo ran."""
    if entry["ginkgo"]:
        g = entry["ginkgo"]
        return g["passed"], g["failed"], g["skipped"]
    tests = leaf_tests(entry["tests"])
    passed = sum(1 for s in tests.values() if s == "pass")
    failed = sum(1 for s in tests.values() if s == "fail")
    skipped = sum(1 for s in tests.values() if s == "skip")
    return passed, failed, skipped


def load_coverage(path):
    """Per-package (covered, total) statement counts from a coverage profile.

    The profile is `mode: <mode>` followed by
    `<file>:<start>,<end> <numStmts> <count>` lines; the package is the
    file's directory. Blocks are deduplicated by position (taking the
    highest count) so a block instrumented by more than one test binary is
    not counted twice.
    """
    blocks = {}
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if not line or line.startswith("mode:"):
                continue
            try:
                position, statements, count = line.rsplit(" ", 2)
                file_name, block = position.rsplit(":", 1)
                statements, count = int(statements), int(count)
            except ValueError:
                continue
            key = (file_name, block)
            previous = blocks.get(key)
            if previous is None or count > previous[1]:
                blocks[key] = (statements, count)

    per_package = defaultdict(lambda: [0, 0])  # covered, total
    for (file_name, _), (statements, count) in blocks.items():
        package = os.path.dirname(file_name)
        per_package[package][1] += statements
        if count > 0:
            per_package[package][0] += statements
    return {k: tuple(v) for k, v in per_package.items()}


def percent(covered, total):
    return f"{100.0 * covered / total:.1f}%" if total else "n/a"


def short(package, module):
    if module and package.startswith(module + "/"):
        return package[len(module) + 1:]
    return package


def failure_output(entry):
    """The test output of a failing package, per-test and total capped."""
    lines = []
    for chunk in entry["output"]:
        lines.extend(chunk.splitlines())
    # Everything from the first failure marker: the preceding output is the
    # passing part of the run and is in the artifact anyway.
    for index, line in enumerate(lines):
        if line.startswith("--- FAIL") or line.strip().startswith("[FAIL]"):
            lines = lines[max(0, index - 5):]
            break
    return lines[:FAILURE_LINES_TOTAL]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "streams",
        nargs="+",
        type=Path,
        help="file(s) holding the output of `go test -json`",
    )
    parser.add_argument(
        "--coverage-profile",
        type=Path,
        help="the -coverprofile file (cover.out), for the coverage column",
    )
    parser.add_argument(
        "--module",
        default="",
        help="module path to strip from package names in the table",
    )
    parser.add_argument("--artifact-url", default="", help="link to the uploaded stream")
    parser.add_argument("--coverage-url", default="", help="link to the coverage artifact")
    parser.add_argument(
        "--title", default="Go test results", help="heading of the report"
    )
    args = parser.parse_args()

    out = []

    def footer():
        links = []
        if args.artifact_url:
            links.append(f"📄 [Full `go test -json` stream]({args.artifact_url})")
        if args.coverage_url:
            links.append(f"📊 [Coverage report]({args.coverage_url})")
        if links:
            out.append("")
            out.append("  ·  ".join(links))

    packages, missing = parse_stream(args.streams)
    # Packages with no test files are reported by `go test` as a package-level
    # skip; they are not failures and have nothing to show.
    tested = {
        name: entry
        for name, entry in packages.items()
        if entry["tests"] or entry["ginkgo"] or entry["result"] == "fail"
    }

    if not tested:
        out.append(f"## {args.title} :warning:")
        out.append("")
        if missing and len(missing) == len(args.streams):
            out.append(
                "No test results: the stream "
                + ", ".join(f"`{name}`" for name in missing)
                + " was never written, so `go test` did not get as far as "
                "running (check the step that runs it)."
            )
        else:
            out.append(
                "No test results in the stream — the run failed before any test "
                "was executed (a build or `go vet` failure, or the generation step)."
            )
        footer()
        print("\n".join(out))
        return 0

    coverage = {}
    if args.coverage_profile and args.coverage_profile.is_file():
        coverage = load_coverage(args.coverage_profile)

    passed = failed = skipped = 0
    for entry in tested.values():
        p, f, s = counts_for(entry)
        passed, failed, skipped = passed + p, failed + f, skipped + s
    total = passed + failed + skipped

    # A package can fail with no failing test: a panic outside a test, a
    # TestMain failure, or a build error. Never report green in that case.
    broken = sorted(
        name
        for name, entry in tested.items()
        if entry["result"] == "fail" and counts_for(entry)[1] == 0
    )

    icon = ":white_check_mark:" if failed == 0 and not broken else ":x:"
    out.append(f"## {args.title} {icon}")
    out.append("")
    out.append(f"**{passed} passed, {failed} failed — {skipped} skipped ({total} tests)**")

    if coverage:
        covered = sum(c for c, _ in coverage.values())
        statements = sum(t for _, t in coverage.values())
        if statements:
            out.append(
                f"Overall coverage: **{percent(covered, statements)}** (statements)"
            )

    if broken:
        out.append("")
        out.append(
            "> :warning: "
            + ", ".join(f"`{short(name, args.module)}`" for name in broken)
            + " failed without a failing test — look for a panic, a `TestMain` "
            "failure or a build error in the output below."
        )

    # Union of packages that ran tests and packages in the coverage profile:
    # a package can be instrumented and still have every test skipped.
    rows = sorted(set(tested) | set(coverage))
    out.append("")
    out.append("| Package | Tests | Time | Coverage |")
    out.append("|---|---:|---:|---:|")
    for name in rows:
        entry = tested.get(name)
        if entry:
            p, f, s = counts_for(entry)
            n = p + f + s
            seconds = (
                entry["ginkgo"]["seconds"]
                if entry["ginkgo"]
                else entry["elapsed"]
            )
        else:
            n, seconds = 0, None
        cover = coverage.get(name)
        out.append(
            "| {package} | {tests} | {time} | {coverage} |".format(
                package=f"`{short(name, args.module)}`",
                tests=str(n) if n else "—",
                time=f"{seconds:.1f}s" if n and seconds is not None else "—",
                coverage=percent(*cover) if cover else "n/a",
            )
        )
    total_coverage = ""
    if coverage:
        covered = sum(c for c, _ in coverage.values())
        statements = sum(t for _, t in coverage.values())
        total_coverage = f"**{percent(covered, statements)}**"
    out.append(f"| **Total** | **{total}** | | {total_coverage} |")

    failing = []
    for name, entry in sorted(tested.items()):
        label = short(name, args.module)
        if entry["ginkgo_failures"]:
            # The specs are the real results; listing the wrapper Go test on
            # top of them would just name the same failure twice.
            failing.extend(f"{label} — {spec}" for spec in entry["ginkgo_failures"])
            continue
        for test, state in sorted(leaf_tests(entry["tests"]).items()):
            if state == "fail":
                failing.append(f"{label} — {test}")
    if failing:
        out.append("")
        out.append("### Failing tests")
        out.append("")
        for item in failing:
            out.append(f"- `{item}`")

    failed_packages = sorted(
        name
        for name, entry in tested.items()
        if entry["result"] == "fail" or counts_for(entry)[1]
    )
    if failed_packages:
        out.append("")
        out.append(
            f"<details><summary>Output of the failing packages "
            f"(truncated to {FAILURE_LINES_TOTAL} lines each)</summary>"
        )
        for name in failed_packages:
            out.append("")
            out.append(f"**`{short(name, args.module)}`**")
            out.append("")
            out.append("```")
            out.extend(failure_output(tested[name]))
            out.append("```")
        out.append("")
        out.append("</details>")

    skipped_tests = []
    for name, entry in sorted(tested.items()):
        label = short(name, args.module)
        for test, state in sorted(leaf_tests(entry["tests"]).items()):
            if state == "skip":
                skipped_tests.append(f"{label} — {test}")
    if skipped_tests:
        out.append("")
        out.append(f"<details><summary>Skipped tests ({len(skipped_tests)})</summary>")
        out.append("")
        for item in skipped_tests:
            out.append(f"- `{item}`")
        out.append("")
        out.append("</details>")

    footer()
    print("\n".join(out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
