#!/usr/bin/env python3
"""Compare scheduler rejection with no accounts, without starting a server.

Exports tracked Go sources to temporary directories. Does not touch a running
Lite2API service, load operational configuration, or contact any upstream.
"""

import argparse
import datetime
import json
import os
from pathlib import Path
import statistics
import subprocess
import tempfile


def run(args, cwd, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--baseline", default="1d6002668554742d153449a00d6b2a558b07c40b")
    parser.add_argument("--current", default="32c2604")
    parser.add_argument("--samples", type=int, default=3)
    parser.add_argument("--wait-seconds", type=int, default=30)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.samples < 1 or args.wait_seconds < 1:
        parser.error("samples and wait-seconds must be positive")
    repo = args.repo.resolve()
    env = dict(os.environ, GOTOOLCHAIN="local", GOCACHE="/tmp/lite2api-case-study-go-cache")
    report = {
        "measured_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "scenario": "Scheduler.Select with no configured accounts or routes",
        "scope": "local scheduler only; not HTTP latency, throughput or a production SLO",
        "wait_seconds": args.wait_seconds,
        "samples_per_revision": args.samples,
        "toolchain": run(["go", "version"], repo, env=env, capture_output=True, text=True).stdout.strip(),
        "revisions": [],
    }
    fixture = '''package gateway

import (
    "context"
    "encoding/json"
    "fmt"
    "testing"
    "time"
    "github.com/lms2004/lite2api/internal/config"
)

func TestCaseStudyNoCandidateMeasurement(t *testing.T) {
    cfg := config.Defaults()
    cfg.Accounts = nil
    cfg.Routes = map[string]config.Route{}
    s := NewScheduler(cfg)
    for i := 0; i < SAMPLE_COUNT; i++ {
        started := time.Now()
        selection, err := SELECT_CALL
        elapsed := float64(time.Since(started).Nanoseconds()) / 1e6
        if selection != nil || err == nil {
            t.Fatalf("unexpected selection=%v err=%v", selection, err)
        }
        raw, _ := json.Marshal(map[string]any{"sample": i+1, "elapsed_ms": elapsed, "error": err.Error()})
        fmt.Printf("CASE_METRIC %s\\n", raw)
    }
}
'''
    for label, ref in (("baseline", args.baseline), ("current", args.current)):
        commit = run(["git", "rev-parse", ref], repo, capture_output=True, text=True).stdout.strip()
        with tempfile.TemporaryDirectory(prefix="lite2api-case-study-") as temp:
            snapshot = Path(temp)
            archive = run(["git", "archive", commit, "go.mod", "internal"], repo, capture_output=True).stdout
            run(["tar", "-x", "-C", str(snapshot)], repo, input=archive)
            operation = "" if label == "baseline" else 'config.OperationOpenAIChat, '
            select_call = ('s.Select(context.Background(), "case-study-no-candidate", '
                           + operation + '"", nil, WAIT_SECONDS*time.Second)')
            source = (fixture.replace("SAMPLE_COUNT", str(args.samples))
                      .replace("SELECT_CALL", select_call)
                      .replace("WAIT_SECONDS", str(args.wait_seconds)))
            (snapshot / "internal/gateway/case_study_measurement_test.go").write_text(source)
            result = run(["go", "test", "./internal/gateway", "-run", "^TestCaseStudyNoCandidateMeasurement$",
                          "-count=1", "-v", f"-timeout={args.samples * args.wait_seconds + 30}s"],
                         snapshot, env=env, capture_output=True, text=True,
                         timeout=args.samples * args.wait_seconds + 120)
            measurements = [json.loads(line.removeprefix("CASE_METRIC "))
                            for line in result.stdout.splitlines() if line.startswith("CASE_METRIC ")]
            if len(measurements) != args.samples:
                raise RuntimeError("measurement sample count mismatch")
            times = [row["elapsed_ms"] for row in measurements]
            report["revisions"].append({"label": label, "commit": commit, "measurements": measurements,
                                        "median_ms": statistics.median(times), "min_ms": min(times),
                                        "max_ms": max(times)})
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
