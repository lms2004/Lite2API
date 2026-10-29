#!/usr/bin/env python3
"""Bounded business acceptance for a running gateway; never prints credentials or bodies."""
import argparse
import json
import os
import signal
import shlex
from pathlib import Path
import time
import urllib.error
import urllib.parse
import urllib.request


PATHS = {
    "openai.chat": "/v1/chat/completions",
    "openai.responses": "/v1/responses",
    "anthropic.messages": "/v1/messages",
    "openai.embeddings": "/v1/embeddings",
    "openai.images": "/v1/images/generations",
    "openai.rerank": "/v1/rerank",
}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def optional_array(value):
    """Absent/null optional collections are empty; other types stay invalid."""
    if value is None:
        return []
    if not isinstance(value, list):
        raise ValueError("optional collection must be an array or null")
    return value


def valid_tools(payload, calls):
    choice = payload.get("tool_choice", payload.get("function_call", "auto"))
    name = ""
    if isinstance(choice, dict):
        kind = choice.get("type", "function" if choice.get("name") else "auto")
        name = choice.get("name") or choice.get("function", {}).get("name", "")
        mode = "required" if kind in ("any", "tool", "function") else kind
    else:
        mode = choice
    if mode == "none" and calls or mode == "required" and not calls:
        return False
    if mode not in ("auto", "none", "required", None):
        return False
    serial = payload.get("parallel_tool_calls") is False or isinstance(choice, dict) and choice.get("disable_parallel_tool_use") is True
    if serial and len(calls) > 1:
        return False
    allowed = {tool.get("function", tool).get("name") for tool in optional_array(payload.get("tools")) if isinstance(tool, dict)}
    allowed.update(tool.get("name") for tool in optional_array(payload.get("functions")) if isinstance(tool, dict))
    identities = set()
    for call in calls:
        if not call.get("name") or name and call["name"] != name or allowed and call["name"] not in allowed:
            return False
        if call.get("id"):
            if call["id"] in identities:
                return False
            identities.add(call["id"])
        arguments = call.get("arguments")
        if isinstance(arguments, str):
            arguments = json.loads(arguments)
        if not isinstance(arguments, dict):
            return False
    return True


def chat_complete(payload, choices):
    expected = payload.get("n", 1)
    if type(expected) is not int or not 1 <= expected <= 128 or len(choices) != expected:
        return False
    indexes = set()
    for choice in choices:
        index = choice.get("index", 0 if expected == 1 else None)
        if type(index) is not int or index in indexes or not 0 <= index < expected:
            return False
        indexes.add(index)
        message = choice.get("message")
        if not isinstance(message, dict) or not choice.get("finish_reason"):
            return False
        calls = []
        for call in optional_array(message.get("tool_calls")):
            if call.get("type") != "function" or not call.get("id"):
                return False
            calls.append({**call.get("function", {}), "id": call["id"]})
        if message.get("function_call"):
            if calls:
                return False
            calls.append(message["function_call"])
        if not valid_tools(payload, calls):
            return False
        if not calls and not (message.get("content") or message.get("refusal")):
            return False
        if calls and choice.get("finish_reason") not in ("tool_calls", "function_call"):
            return False
    return True


def terminal_complete(operation, payload, result):
    if result.get("error"):
        return False
    if operation == "openai.chat":
        return chat_complete(payload, result.get("choices", []))
    if operation == "openai.responses":
        output = result.get("output")
        if result.get("status") != "completed" or not isinstance(output, list) or not output:
            return False
        calls = [{"id": item.get("call_id"), "name": item.get("name"), "arguments": item.get("arguments")} for item in output if item.get("type") == "function_call"]
        if not calls and not any(item.get("type") == "message" and any(part.get("text") or part.get("refusal") for part in item.get("content", [])) for item in output):
            return False
        return all(isinstance(call["id"], str) and call["id"] for call in calls) and valid_tools(payload, calls)
    if operation == "anthropic.messages":
        content = result.get("content")
        if not result.get("stop_reason") or not isinstance(content, list) or not content:
            return False
        calls = [{"id": item.get("id"), "name": item.get("name"), "arguments": item.get("input")} for item in content if item.get("type") == "tool_use"]
        if not calls and not any(item.get("text") or item.get("thinking") or item.get("data") for item in content):
            return False
        return bool(calls) == (result.get("stop_reason") == "tool_use") and all(isinstance(call["id"], str) and call["id"] for call in calls) and valid_tools(payload, calls)
    if operation == "openai.embeddings":
        return bool(result.get("data")) and all(isinstance(i.get("embedding"), (list, str)) and bool(i["embedding"]) for i in result["data"])
    if operation == "openai.images":
        return bool(result.get("data")) and all(i.get("url") or i.get("b64_json") for i in result["data"])
    return operation == "openai.rerank" and isinstance(result.get("results"), list)


def protocol_complete(operation, payload, body):
    """Check actual protocol artifacts, independently of status and gateway counters."""
    stream = payload.get("stream", False)
    frames = []
    done = False
    if stream:
        events = body.decode("utf-8").replace("\r\n", "\n").split("\n\n")
        # EOF does not dispatch an SSE event. Only comments and ignored fields
        # may remain after the last blank-line separator.
        if any(line.partition(":")[0] in ("data", "event") for line in events[-1].splitlines()):
            return False
        for event in events[:-1]:
            data = "\n".join(line[5:].lstrip(" ") for line in event.splitlines() if line.startswith("data:"))
            if not data:
                continue
            if data == "[DONE]":
                # A terminal marker commits the stream.  Accepting later
                # frames would let a malformed or failing tail masquerade as
                # a completed release check.
                if operation != "openai.chat" or done:
                    return False
                done = True
                continue
            if done:
                return False
            frame = json.loads(data)
            if frame.get("error") or frame.get("type") in ("error", "response.failed", "response.incomplete"):
                return False
            frames.append(frame)
        if operation == "openai.chat":
            choices, tools = {}, {}
            for frame in frames:
                for choice in frame.get("choices", []):
                    index = choice.get("index", 0 if payload.get("n", 1) == 1 else None)
                    if type(index) is not int or not 0 <= index < 128:
                        return False
                    row = choices.setdefault(index, {"index": index, "message": {"content": ""}, "finish_reason": None})
                    delta = choice.get("delta", {})
                    row["message"]["content"] += delta.get("content") or delta.get("refusal") or ""
                    for tool in optional_array(delta.get("tool_calls")):
                        tool_index = tool.get("index")
                        if type(tool_index) is not int or not 0 <= tool_index < 128:
                            return False
                        current = tools.setdefault((index, tool_index), {"id": "", "type": "function", "function": {"name": "", "arguments": ""}})
                        if tool.get("id"):
                            if current["id"] and current["id"] != tool["id"]:
                                return False
                            current["id"] = tool["id"]
                        for field in ("name", "arguments"):
                            current["function"][field] += tool.get("function", {}).get(field, "")
                            if len(current["function"][field]) > 1 << 20:
                                return False
                    if choice.get("finish_reason"):
                        row["finish_reason"] = choice["finish_reason"]
            for (index, _), tool in tools.items():
                choices[index]["message"].setdefault("tool_calls", []).append(tool)
            return done and chat_complete(payload, list(choices.values()))
        if operation == "openai.responses":
            terminal_frames = [f for f in frames if f.get("type") == "response.completed"]
            if len(terminal_frames) != 1:
                return False
            terminal_index = next(index for index, frame in enumerate(frames) if frame.get("type") == "response.completed")
            if terminal_index != len(frames) - 1:
                return False
            return terminal_complete(operation, payload, terminal_frames[0].get("response", {}))
        if operation == "anthropic.messages":
            message, blocks, partial, stopped = None, {}, {}, set()
            for frame in frames:
                kind = frame.get("type")
                if kind == "message_start":
                    if message is not None:
                        return False
                    message = dict(frame.get("message", {}))
                elif kind in ("content_block_start", "content_block_delta", "content_block_stop"):
                    index = frame.get("index")
                    if type(index) is not int or not 0 <= index < 128:
                        return False
                    if kind == "content_block_start":
                        if index in blocks:
                            return False
                        blocks[index] = dict(frame.get("content_block", {}))
                    elif index not in blocks or index in stopped:
                        return False
                    elif kind == "content_block_stop":
                        stopped.add(index)
                    else:
                        delta = frame.get("delta", {})
                        expected = {
                            "input_json_delta": ("tool_use", "partial_json"),
                            "text_delta": ("text", "text"),
                            "thinking_delta": ("thinking", "thinking"),
                            "signature_delta": ("thinking", "signature"),
                        }.get(delta.get("type"))
                        if expected and (blocks[index].get("type") != expected[0]
                                         or not isinstance(delta.get(expected[1]), str)):
                            return False
                        if delta.get("type") == "input_json_delta":
                            partial[index] = partial.get(index, "") + delta.get("partial_json", "")
                            if len(partial[index]) > 1 << 20:
                                return False
                        for field in ("text", "thinking"):
                            if field in delta:
                                if not isinstance(delta[field], str):
                                    return False
                                blocks[index][field] = blocks[index].get(field, "") + delta[field]
                elif kind == "message_delta" and message is not None:
                    message["stop_reason"] = frame.get("delta", {}).get("stop_reason")
            stops = [index for index, frame in enumerate(frames) if frame.get("type") == "message_stop"]
            if message is None or set(blocks) != stopped or len(stops) != 1 or stops[0] != len(frames) - 1:
                return False
            for index, value in partial.items():
                blocks[index]["input"] = json.loads(value)
            message["content"] = [blocks[index] for index in sorted(blocks)]
            return terminal_complete(operation, payload, message)
        return False
    return terminal_complete(operation, payload, json.loads(body))


def verify(base, cases, environ=None, timeout=90):
    environ = os.environ if environ is None else environ
    parsed = urllib.parse.urlsplit(base)
    if parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ValueError("acceptance URL must have a safe HTTP(S) origin and path")
    if parsed.scheme == "http" and parsed.hostname not in ("localhost", "127.0.0.1", "::1"):
        raise ValueError("unencrypted acceptance is restricted to loopback")
    if not 1 <= len(cases) <= 32 or not any(c.get("expect_status", 200) == 200 for c in cases):
        raise ValueError("acceptance requires 1..32 cases including a completed call")
    opener = urllib.request.build_opener(NoRedirect())
    deadline = time.monotonic() + timeout
    results = []

    def call(name, path, payload, key, expected, operation=None, expected_account=None):
        remaining = deadline - time.monotonic()
        row = {"name": name, "operation": operation, "expected_status": expected, "ok": False}
        if remaining <= 0:
            row["reason"] = "acceptance_deadline"
            results.append(row)
            return
        headers = {"Content-Type": "application/json", "anthropic-version": "2023-06-01"}
        if key:
            headers["Authorization"] = "Bearer " + key
        request = urllib.request.Request(base.rstrip("/") + path, data=None if payload is None else json.dumps(payload).encode(), headers=headers)
        started = time.monotonic()
        try:
            try:
                response = opener.open(request, timeout=min(remaining, 45))
            except urllib.error.HTTPError as error:
                response = error
            with response:
                body = response.read((8 << 20) + 1)
                row["status"] = response.status
                logical_id = response.headers.get("X-Lite2API-Request-Id")
                if logical_id:
                    row["request_id"] = logical_id
            row["ok"] = row["status"] == expected and len(body) <= 8 << 20
            if row["ok"] and expected == 200 and payload is not None:
                row["complete"] = bool(protocol_complete(operation, payload, body))
                row["ok"] = row["complete"]
            if row["ok"] and expected_account:
                admin = environ.get("LITE2API_ADMIN_TOKEN")
                if not admin or not logical_id:
                    row["ok"] = False
                else:
                    state_request = urllib.request.Request(base.rstrip("/") + "/admin/api/state", headers={"Authorization": "Bearer " + admin})
                    with opener.open(state_request, timeout=min(max(deadline-time.monotonic(), 0.1), 10)) as state_response:
                        encoded = state_response.read((32 << 20) + 1)
                        if len(encoded) > 32 << 20:
                            raise ValueError("state response exceeds acceptance budget")
                        state = json.loads(encoded)
                    observed = next((r for r in state["stats"]["recent"] if r["request_id"] == logical_id), None)
                    row["source_confirmed"] = bool(observed and observed.get("account_id") == expected_account and observed.get("outcome") == "success")
                    row["ok"] = row["source_confirmed"]
            if not row["ok"]:
                row["reason"] = "unexpected_status_or_incomplete_result"
        except (OSError, ValueError, TypeError, KeyError, AttributeError, IndexError):
            row["ok"] = False
            row["reason"] = "transport_or_protocol_failure"
        row["latency_ms"] = round((time.monotonic() - started) * 1000)
        results.append(row)

    # A live process with anonymous model access is never a valid release.
    call("anonymous_access_rejected", "/v1/models", None, "", 401)
    if environ.get("LITE2API_ADMIN_TOKEN"):
        call("admin_credential_separated", "/v1/models", None, environ["LITE2API_ADMIN_TOKEN"], 401)
    for index, case in enumerate(cases):
        operation = case.get("operation", "openai.chat")
        if operation not in PATHS:
            raise ValueError("acceptance operation is unsupported")
        payload = dict(case.get("request", {}))
        payload["model"] = case["model"]
        payload["stream"] = bool(case.get("stream", False))
        if operation == "openai.responses":
            payload.setdefault("input", "Reply with OK.")
            payload.setdefault("max_output_tokens", 64)
        elif operation in ("openai.chat", "anthropic.messages"):
            payload.setdefault("messages", [{"role": "user", "content": "Reply with OK."}])
            payload.setdefault("max_tokens", 64)
        key_name = case.get("key_env", "LITE2API_API_KEYS")
        key = environ.get(key_name, "").split(",")[0].strip() if key_name else ""
        if key_name and not key:
            raise ValueError("acceptance credential environment variable is missing")
        call(case.get("name", "case_" + str(index + 1)), PATHS[operation], payload, key, case.get("expect_status", 200), operation, case.get("expect_account"))
    return {"ok": all(row["ok"] for row in results), "cases": results}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--cases", required=True)
    parser.add_argument("--output")
    parser.add_argument("--env-file", help="Read credential assignments as data; never execute shell text")
    parser.add_argument("--timeout", type=int, default=90)
    args = parser.parse_args()
    def timed_out(_signum, _frame):
        raise TimeoutError("acceptance deadline")
    signal.signal(signal.SIGALRM, timed_out)
    try:
        if not 1 <= args.timeout <= 300:
            raise ValueError("acceptance timeout must be 1..300 seconds")
        document = json.loads(Path(args.cases).read_text())
        if document.get("version") != 1:
            raise ValueError("acceptance manifest version must be 1")
        environ = dict(os.environ)
        if args.env_file:
            for line in Path(args.env_file).read_text().splitlines():
                if not line.strip() or line.lstrip().startswith("#"):
                    continue
                fields = shlex.split(line, comments=True)
                if len(fields) != 1 or "=" not in fields[0]:
                    raise ValueError("invalid environment assignment")
                name, value = fields[0].split("=", 1)
                environ[name] = value
        signal.setitimer(signal.ITIMER_REAL, args.timeout)
        result = verify(args.url, document["cases"], environ=environ, timeout=args.timeout)
    except (OSError, ValueError, KeyError, TypeError, AttributeError, IndexError):
        result = {"ok": False, "reason": "invalid_acceptance_configuration"}
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
    encoded = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        path = Path(args.output)
        with open(path, "w", opener=lambda p, flags: os.open(p, flags, 0o600)) as output:
            output.write(encoded)
    print(encoded, end="")
    raise SystemExit(0 if result["ok"] else 1)


if __name__ == "__main__":
    main()
