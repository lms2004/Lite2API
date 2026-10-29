#!/usr/bin/env python3
"""Accept an isolated build against existing sources, retaining summaries only.

This starts no production service and changes no production configuration. It
makes bounded, real model requests (and therefore may consume upstream quota).
Credentials stay in a private temporary directory/process environment; neither
raw prompts/responses nor credential values are written to the evidence file.
"""
import argparse
import datetime
import json
import os
from pathlib import Path
import secrets
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

REPO_ROOT = Path(__file__).resolve().parents[1]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))
from deploy.verify_service import protocol_complete


class AcceptanceInterrupted(BaseException):
    pass


def interrupt_acceptance(signum, _frame):
    raise AcceptanceInterrupted(signum)


def complete(operation, body, payload):
    protocol_operation = {"chat": "openai.chat", "responses": "openai.responses", "messages": "anthropic.messages"}[operation]
    return protocol_complete(protocol_operation, payload, body.encode("utf-8"))


def run_bounded(command, *, timeout, input=None, **kwargs):
    """Bound clients and their tool children, including cancellation and timeout."""
    process = subprocess.Popen(command, stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               start_new_session=True, **kwargs)
    try:
        stdout, stderr = process.communicate(input, timeout=timeout)
        return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)
    finally:
        # A client may exit while a tool child still holds a socket or a pipe.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)
        for pipe in (process.stdin, process.stdout, process.stderr):
            if pipe is not None:
                pipe.close()


def source_candidates(config, alias, operation):
    """Select only enabled route members; preserve the reviewed model/credential scope.

    In particular, an omitted enabled field means false in the Go configuration.
    Wildcard routing does not authorize probing an unrelated advertised model.
    The server remains authoritative for capability and feature validation.
    """
    route = config["routes"][alias]
    accounts = {account["id"]: account for account in config.get("accounts", [])}
    if route.get("targets"):
        targets = route["targets"]
    elif route.get("accounts"):
        targets = [{"account": name} for name in route["accounts"]]
    elif route.get("all_accounts") is True:
        targets = [{"account": name} for name in accounts]
    else:
        return []
    candidates = []
    for target in targets:
        account = accounts.get(target.get("account"))
        if not account or account.get("enabled") is not True:
            continue
        defaults = ["anthropic.messages"] if account.get("type", "").strip().lower() == "anthropic" else ["openai.chat", "openai.responses", "openai.embeddings", "openai.images", "openai.rerank"]
        if operation not in (account.get("operations") or defaults):
            continue
        effort = target.get("reasoning_effort") or "auto"
        capabilities = account.get("capabilities") or []
        if route.get("model"):
            effort = route.get("reasoning_effort") or "auto"
            capability = next((cap for cap in capabilities if cap.get("model") == route["model"] and cap.get("evidence", {}).get("status") != "unsupported" and effort in (cap.get("reasoning_efforts") or ["auto"])), None)
            if not capability:
                continue
            model = capability["upstream_model"]
        elif route.get("targets"):
            model = target.get("model")
        else:
            model = route.get("upstream_model") or (account.get("model_map") or {}).get(alias)
            if not model:
                capability = next((cap for cap in capabilities if cap.get("model") == alias and cap.get("evidence", {}).get("status") != "unsupported"), None)
                model = capability.get("upstream_model") if capability else alias
        declared = account.get("models") or []
        if not model or model == "*" or declared and "*" not in declared and model not in declared and model not in (account.get("model_map") or {}).values():
            continue
        matching = [cap for cap in capabilities if cap.get("upstream_model") == model]
        if matching and all(cap.get("evidence", {}).get("status") == "unsupported" for cap in matching):
            continue
        if operation == "anthropic.messages" and effort != "auto":
            continue
        candidates.append((account, model, effort, target.get("credential", "")))
    return candidates


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--source-config", required=True)
    parser.add_argument("--source-env", required=True)
    parser.add_argument("--route", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--claude", action="store_true")
    parser.add_argument("--codex", action="store_true")
    parser.add_argument("--source-verification", action="store_true", help="Verify exact-source streams and tool roundtrips and save evidence in the private test config")
    parser.add_argument("--tool-clients", action="store_true", help="Require real clients to read an isolated nonce file through their tools")
    parser.add_argument("--only", nargs="*", help="Restrict curl acceptance to named cases; an empty list runs only the requested source/client checks")
    parser.add_argument("--source-operations", nargs="+", choices=["openai.chat", "openai.responses", "anthropic.messages"], default=["openai.chat", "openai.responses", "anthropic.messages"])
    parser.add_argument("--source-features", nargs="+", choices=["call", "stream", "tools", "image_input"], default=["stream", "tools", "image_input"])
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, interrupt_acceptance)
    signal.signal(signal.SIGHUP, interrupt_acceptance)
    allowed_only = {"chat_json", "chat_stream", "responses_json", "responses_stream", "messages_json", "messages_stream"}
    unknown_only = sorted(set(args.only or ()) - allowed_only)
    if unknown_only:
        parser.error("unknown --only case(s): " + ", ".join(unknown_only) + "; choose from " + ", ".join(sorted(allowed_only)))
    report = {"observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "route": args.route, "isolation": "temporary_config_keys_logs_and_port", "cases": []}
    env = dict(os.environ)
    # Parse assignments as data, never source or execute a shell environment.
    for line in Path(args.source_env).read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        parts = shlex.split(line, comments=True)
        if parts and parts[0] == "export":
            parts = parts[1:]
        for part in parts:
            name, sep, value = part.partition("=")
            if sep:
                env[name] = value
    # Acceptance must not trigger an administrative refresh of production auth.
    env["CLIPROXYAPI_MANAGEMENT_KEY"] = ""
    env["LITE2API_ADMIN_AUTO_LOGIN"] = "false"
    config = json.loads(Path(args.source_config).read_text())
    if args.route not in config.get("routes", {}):
        raise SystemExit("acceptance route is not configured")
    with tempfile.TemporaryDirectory(prefix="lite2api-acceptance-") as directory:
        root = Path(directory)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        key = secrets.token_urlsafe(32)
        server = config["server"]
        server.update(listen=f"127.0.0.1:{port}", api_keys=[key], api_key_env="", admin_token=secrets.token_urlsafe(32), admin_token_env="", client_keys_path=str(root / "keys.json"), request_log_path=str(root / "requests.jsonl"), max_inflight_requests=4, queue_timeout="3s")
        # Normalize supplies the standard env names for empty fields. Override
        # those too, so inherited production credentials cannot authenticate
        # against this private acceptance instance or shadow its test token.
        env["LITE2API_API_KEYS"] = key
        env["LITE2API_ADMIN_TOKEN"] = server["admin_token"]
        config_path = root / "config.json"
        config_path.touch(mode=0o600)
        config_path.write_text(json.dumps(config))
        with (root / "process.log").open("wb") as log:
            process = subprocess.Popen([str(Path(args.binary).resolve()), "-config", str(config_path)], env=env, stdout=log, stderr=log, start_new_session=True)
            try:
                base = f"http://127.0.0.1:{port}"
                for _ in range(100):
                    if process.poll() is not None:
                        raise RuntimeError("isolated gateway exited during startup")
                    try:
                        with urllib.request.urlopen(base + "/livez", timeout=0.3) as response:
                            if response.status == 200:
                                break
                    except (OSError, TimeoutError):
                        time.sleep(0.05)
                else:
                    raise RuntimeError("isolated gateway did not become live")
                report["binary_sha256"] = __import__("hashlib").sha256(Path(args.binary).read_bytes()).hexdigest()
                for operation, path in [("chat", "/chat/completions"), ("responses", "/responses"), ("messages", "/messages")]:
                    for stream in (False, True):
                        name = operation + ("_stream" if stream else "_json")
                        if args.only is not None and name not in args.only:
                            continue
                        payload = {"model": args.route, "stream": stream}
                        if operation == "responses":
                            payload.update(input="Reply with OK.", max_output_tokens=64)
                        else:
                            payload.update(messages=[{"role": "user", "content": "Reply with OK."}], max_tokens=64)
                        started = time.monotonic()
                        result = run_bounded(["curl", "--config", "-", "--max-time", "45", "--silent", "--show-error", "--write-out", "\n%{http_code}", "-H", "Content-Type: application/json", "-H", "anthropic-version: 2023-06-01", "--data-binary", json.dumps(payload), base + "/v1" + path], input=f'header = "Authorization: Bearer {key}"\n', text=True, timeout=50)
                        body, _, status = result.stdout.rpartition("\n")
                        case = {"case": name, "client": "curl", "operation": operation, "stream": stream, "http_status": int(status or 0), "transport_exit": result.returncode, "latency_ms": round((time.monotonic()-started)*1000), "complete": False}
                        case["body_bytes"] = len(body.encode())
                        case["body_sha256"] = __import__("hashlib").sha256(body.encode()).hexdigest()
                        try:
                            case["complete"] = result.returncode == 0 and case["http_status"] == 200 and bool(complete(operation, body, payload))
                            if case["http_status"] != 200:
                                error = json.loads(body).get("error", {})
                                case["error_code"] = error.get("code", error.get("type", "unknown"))
                        except (ValueError, KeyError, TypeError):
                            case["error_code"] = "unparseable_protocol_result"
                        report["cases"].append(case)
                if args.source_verification:
                    route = config["routes"][args.route]
                    for operation in args.source_operations:
                        candidates = source_candidates(config, args.route, operation)
                        if not candidates:
                            report["cases"].append({"client": "exact_source_verification", "operation": operation, "complete": False, "error_code": "no_eligible_configured_source"})
                            continue
                        account, model, effort, credential = candidates[0]
                        account_id = account["id"]
                        for feature in args.source_features:
                            payload = {"account_id": account_id, "model": model, "operation": operation, "feature": feature, "reasoning_effort": effort, "credential_id": credential}
                            started = time.monotonic()
                            result = run_bounded(["curl", "--config", "-", "--max-time", "65", "--silent", "--show-error", "--write-out", "\n%{http_code}", "-H", "Content-Type: application/json", "--data-binary", json.dumps(payload), base+"/admin/api/accounts/verify"], input='header = "Authorization: Bearer '+server['admin_token']+'"\n', text=True, timeout=70)
                            body, _, status = result.stdout.rpartition("\n")
                            case = {"client": "exact_source_verification", "operation": operation, "feature": feature, "account_id": account_id, "http_status": int(status or 0), "complete": False, "latency_ms": round((time.monotonic()-started)*1000)}
                            try:
                                verification = json.loads(body)
                                case.update({name: verification.get(name) for name in ("completed", "evidence_saved", "credential_id", "upstream_model", "observed_at", "request_id")})
                                case["complete"] = result.returncode == 0 and case["http_status"] == 200 and verification.get("ok") is True and verification.get("completed") is True and verification.get("evidence_saved") is True
                            except (ValueError, TypeError):
                                case["error_code"] = "invalid_verification_response"
                            report["cases"].append(case)
                # A coding client needs only its test gateway credential. Source
                # and administrator secrets must not enter its tool environment.
                public_environment = {name: os.environ[name] for name in ("PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR") if name in os.environ}
                client_directory = root / "client-work"
                client_directory.mkdir(mode=0o700)
                nonce = secrets.token_hex(20)
                (client_directory / "acceptance-nonce.txt").write_text(nonce+"\n")
                if args.claude:
                    client_env = dict(public_environment)
                    client_env.update(ANTHROPIC_BASE_URL=base, ANTHROPIC_API_KEY=key, CLAUDE_CONFIG_DIR=str(root / "claude"), DISABLE_TELEMETRY="1", DISABLE_ERROR_REPORTING="1", CLAUDE_CODE_MAX_RETRIES="0", CLAUDE_CODE_MAX_OUTPUT_TOKENS="1024", MAX_THINKING_TOKENS="0")
                    client_env.pop("ANTHROPIC_AUTH_TOKEN", None)
                    client_env.pop("CLAUDE_CODE_OAUTH_TOKEN", None)
                    started = time.monotonic()
                    try:
                        command = ["claude", "--bare", "--no-session-persistence", "--model", args.route, "--max-turns", "3" if args.tool_clients else "1", "--output-format", "stream-json" if args.tool_clients else "json", "--system-prompt", "You are an API connectivity check. Give a short answer."]
                        if args.tool_clients:
                            command[5:5] = ["--tools", "Read"]
                        prompt = "Reply with OK."
                        if args.tool_clients:
                            command += ["--verbose", "--allowedTools", "Read"]
                            prompt = "Use Read once to read acceptance-nonce.txt in the current directory. Return only its single value. Do not inspect other files."
                        result = run_bounded(command+["-p", prompt], env=client_env, cwd=client_directory, text=True, timeout=65)
                        if args.tool_clients:
                            events = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
                            output = next((event for event in reversed(events) if event.get("type") == "result"), {})
                            read_observed = any(block.get("type") == "tool_use" and block.get("name") == "Read" and Path(block.get("input", {}).get("file_path", "")).name == "acceptance-nonce.txt" for event in events for block in event.get("message", {}).get("content", []) if isinstance(block, dict))
                            success = read_observed and nonce in output.get("result", "")
                        else:
                            output = json.loads(result.stdout)
                            success = bool(output.get("result"))
                        report["cases"].append({"client": "Claude Code", "operation": "messages", "tool_roundtrip": args.tool_clients, "transport_exit": result.returncode, "complete": result.returncode == 0 and not output.get("is_error", True) and success, "latency_ms": round((time.monotonic()-started)*1000)})
                    except (OSError, subprocess.TimeoutExpired, ValueError, TypeError, AttributeError):
                        report["cases"].append({"client": "Claude Code", "operation": "messages", "complete": False, "error_code": "client_timeout_or_invalid_output"})
                if args.codex:
                    client_env = dict(public_environment)
                    client_env["LITE2API_ACCEPTANCE_KEY"] = key
                    output_path = root / "codex-result.txt"
                    started = time.monotonic()
                    command = ["codex", "--no-daemon", "exec", "--ignore-user-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--model", args.route, "--output-last-message", str(output_path)]
                    for setting in [
                        'model_provider="acceptance"',
                        'model_providers.acceptance.name="Lite2API isolated acceptance"',
                        'model_providers.acceptance.base_url="' + base + '/v1"',
                        'model_providers.acceptance.env_key="LITE2API_ACCEPTANCE_KEY"',
                        'model_providers.acceptance.wire_api="responses"',
                        'model_providers.acceptance.requires_openai_auth=false',
                        'model_providers.acceptance.supports_websockets=false',
                        'model_providers.acceptance.request_max_retries=0',
                        'model_providers.acceptance.stream_max_retries=0',
                        'model_providers.acceptance.stream_idle_timeout_ms=45000',
                    ]:
                        command.extend(["-c", setting])
                    if args.tool_clients:
                        command += ["--json", "Run exactly cat acceptance-nonce.txt once in this directory. Then return only its single value. Do not inspect other files or run other commands."]
                    else:
                        command.append("Reply exactly OK. Do not call tools or inspect files.")
                    try:
                        result = run_bounded(command, env=client_env, cwd=client_directory, text=True, timeout=65)
                        answer = output_path.read_text().strip() if output_path.exists() else ""
                        executed = True
                        if args.tool_clients:
                            events = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
                            executed = any(event.get("item", {}).get("type") == "command_execution" and event["item"].get("exit_code") == 0 and event["item"].get("aggregated_output", "").strip() == nonce for event in events)
                        report["cases"].append({"client": "Codex CLI", "operation": "responses", "tool_roundtrip": args.tool_clients, "transport_exit": result.returncode, "complete": result.returncode == 0 and bool(answer) and executed and (not args.tool_clients or nonce in answer), "latency_ms": round((time.monotonic()-started)*1000)})
                    except (OSError, subprocess.TimeoutExpired, ValueError, TypeError, AttributeError):
                        report["cases"].append({"client": "Codex CLI", "operation": "responses", "complete": False, "error_code": "client_timeout_or_invalid_output"})
            except (AcceptanceInterrupted, KeyboardInterrupt, OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
                report["runtime_error"] = type(error).__name__
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        report["source_evidence"] = []
        if args.source_verification:
            observed_config = json.loads(config_path.read_text())
            for account in observed_config["accounts"]:
                for capability in account.get("capabilities", []):
                    for scope, fact in capability.get("features", {}).items():
                        if str(fact.get("source", "")).startswith("capability_test:"):
                            report["source_evidence"].append({"account_id": account["id"], "scope": scope, **{name: fact.get(name) for name in ("status", "source", "source_identity", "upstream_model", "credential_id", "reasoning_effort", "observed_at", "last_check_outcome")}})
        report["logical_calls"] = []
        if (root / "requests.jsonl").exists():
            for line in (root / "requests.jsonl").read_text().splitlines():
                row = json.loads(line)
                summary = {name: row.get(name) for name in ("request_id", "operation", "stream", "status", "outcome", "account_id", "upstream_model", "credential_id", "attempt_count")}
                if row.get("error", "").startswith("upstream completion check failed:"):
                    summary["completion_error"] = row["error"][:256]
                report["logical_calls"].append(summary)
    required = [case for case in report["cases"] if case.get("client") in ("curl", "exact_source_verification")]
    if args.claude:
        required.extend(case for case in report["cases"] if case.get("client") == "Claude Code")
    if args.codex:
        required.extend(case for case in report["cases"] if case.get("client") == "Codex CLI")
    if args.source_verification and not any(case.get("client") == "exact_source_verification" for case in report["cases"]):
        report["source_verification_error"] = "no configured source operation matched the selected route"
    report["ok"] = bool(required) and all(bool(case.get("complete")) for case in required) and not report.get("source_verification_error") and not report.get("runtime_error")
    Path(args.output).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"ok": report["ok"], "cases": len(report["cases"]), "complete": sum(bool(c.get("complete")) for c in report["cases"]), "output": args.output}))
    raise SystemExit(0 if report["ok"] else 1)


if __name__ == "__main__":
    main()
