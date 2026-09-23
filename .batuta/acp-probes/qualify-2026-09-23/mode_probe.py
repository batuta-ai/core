#!/usr/bin/env python3
"""Raw ACP probe: select a session mode, run one prompt, answer permission
requests with a policy, log every message. Usage:
  mode_probe.py <bridge-cmd> <mode> <policy deny|workspace> <case task|shell|outside> <log>
"""
import json, os, subprocess, sys, tempfile, threading, time

cmd, mode, policy, case, log = sys.argv[1:6]
cwd = os.path.realpath(tempfile.mkdtemp(prefix="acp-mode-"))
if os.environ.get("CLAUDE_SANDBOX") == "file":
    os.makedirs(os.path.join(cwd, ".claude"))
    json.dump({"sandbox": {"enabled": True, "autoAllowBashIfSandboxed": True}}, open(os.path.join(cwd, ".claude", "settings.json"), "w"))
outside = tempfile.mkdtemp(prefix=".batuta-qualify-", dir=os.path.expanduser("~"))
prompts = {
    "task": "Create a file named artifact.txt in the current directory containing exactly the text batuta-native-acp-ok followed by one newline. Do nothing else.",
    "shell": "Run exactly this shell command in the current directory and nothing else: sh -c 'echo shell-ok > shell.txt'",
    "shell-outside": "Run exactly this shell command and nothing else: sh -c 'echo x > OUTSIDE/denied.txt'",
    "outside": f"Create a file at the absolute path {outside}/denied.txt containing the word denied. Do nothing else.",
}
p = subprocess.Popen([cmd], cwd=cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1)
out = open(log, "w")
pending, results, lock, nid = {}, {}, threading.Lock(), [0]
decisions = []

def send(msg):
    out.write(">> " + json.dumps(msg) + "\n"); out.flush()
    p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()

def call(method, params, timeout=120):
    with lock:
        nid[0] += 1; i = nid[0]
        ev = threading.Event(); pending[i] = ev
    send({"jsonrpc": "2.0", "id": i, "method": method, "params": params})
    if not ev.wait(timeout):
        raise TimeoutError(method)
    return results.pop(i)

def inside(path):
    # Same walk as executor.resolveWorktreeLocation: no "..", absolute only,
    # skip only components that do not exist, fail closed on unresolvable links.
    if ".." in path.replace("\\", "/").split("/") or not os.path.isabs(path):
        return False
    current, rest = os.path.normpath(path), []
    while True:
        try:
            os.lstat(current)
        except FileNotFoundError:
            parent = os.path.dirname(current)
            if parent == current:
                return False
            rest.insert(0, os.path.basename(current)); current = parent
            continue
        except OSError:
            return False
        try:
            real = os.path.realpath(current, strict=True)
        except OSError:
            return False
        real = os.path.join(real, *rest)
        return real == cwd or real.startswith(cwd + os.sep)

def answer(msg):
    tc = msg["params"].get("toolCall", {})
    opts = msg["params"].get("options", [])
    locs = [l.get("path", "") for l in tc.get("locations") or []]
    allow = policy == "workspace" and locs and all(inside(l) for l in locs)
    kind = "allow_once" if allow else "reject_once"
    choice = next((o for o in opts if o.get("kind") == kind), None)
    outcome = {"outcome": "selected", "optionId": choice["optionId"]} if choice else {"outcome": "cancelled"}
    decisions.append({"kind": tc.get("kind"), "locations": locs, "allowed": bool(allow)})
    send({"jsonrpc": "2.0", "id": msg["id"], "result": {"outcome": outcome}})

def redact(line):
    # _auth/status_update carries the signed-in account; never keep it.
    if '"_auth/status_update"' in line:
        return '{"method":"_auth/status_update","params":"redacted"}\n'
    # The command list carries the host's local skills and commands.
    if '"available_commands_update"' in line:
        return '{"method":"session/update","params":{"update":{"sessionUpdate":"available_commands_update","availableCommands":"redacted"}}}\n'
    return line

def reader():
    for line in p.stdout:
        out.write("<< " + redact(line)); out.flush()
        try: msg = json.loads(line)
        except ValueError: continue
        if "method" in msg and "id" in msg:
            if msg["method"] == "session/request_permission": answer(msg)
            else: send({"jsonrpc": "2.0", "id": msg["id"], "error": {"code": -32601, "message": "unsupported"}})
        elif "id" in msg and msg["id"] in pending:
            results[msg["id"]] = msg; pending.pop(msg["id"]).set()

threading.Thread(target=reader, daemon=True).start()
try:
    started = time.time()
    call("initialize", {"protocolVersion": 1, "clientCapabilities": {}, "clientInfo": {"name": "batuta-probe", "version": "1"}})
    new = {"cwd": cwd, "mcpServers": []}
    if os.environ.get("CLAUDE_SANDBOX") == "meta":
        new["_meta"] = {"claudeCode": {"options": {"sandbox": {"enabled": True, "autoAllowBashIfSandboxed": True}}}}
    sid = call("session/new", new)["result"]["sessionId"]
    modeset = call("session/set_config_option", {"sessionId": sid, "configId": "mode", "value": mode})
    current = [o.get("currentValue") for o in modeset.get("result", {}).get("configOptions", []) if o.get("id") == "mode"]
    cheap = {"codex-acp": [("model", "gpt-5.6-sol"), ("reasoning_effort", "low")], "claude-agent-acp": [("model", "haiku")]}
    for cid, val in cheap.get(os.path.basename(cmd), []):
        call("session/set_config_option", {"sessionId": sid, "configId": cid, "value": val})
    res = call("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": prompts[case].replace("OUTSIDE", outside)}]}, timeout=150)
    time.sleep(0.5)
    report = {
        "bridge": cmd, "mode_requested": mode, "mode_confirmed": current, "policy": policy, "case": case,
        "elapsed_ms": int((time.time() - started) * 1000), "stop": res.get("result", {}).get("stopReason"), "error": res.get("error"),
        "decisions": decisions,
        "artifact": open(os.path.join(cwd, "artifact.txt")).read() if os.path.exists(os.path.join(cwd, "artifact.txt")) else None,
        "shell": open(os.path.join(cwd, "shell.txt")).read() if os.path.exists(os.path.join(cwd, "shell.txt")) else None,
        "outside_written": os.path.exists(os.path.join(outside, "denied.txt")),
    }
    print(json.dumps(report))
    out.write("## " + json.dumps(report) + "\n")
finally:
    p.terminate()
    try:
        p.wait(5)
    except subprocess.TimeoutExpired:
        p.kill()
    out.close()
    subprocess.run(["rm", "-rf", outside, cwd])
