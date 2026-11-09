"""Start an isolated installation, check the embedded UI, and verify SQLite restart persistence."""
import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--launcher", type=Path, help="Optional Unix start.sh to exercise signal forwarding")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="prtseek-smoke-") as directory:
        root = Path(directory)
        env = dict(os.environ, prtseek_DB_PATH=str(root / "state.db"),
                   prtseek_CONFIG=str(root / "missing-config.json"), prtseek_SKILL_DIR=str(root / "skills"))
        port = free_port()
        base = f"http://127.0.0.1:{port}"
        command = [str(binary)] if not args.launcher else ["sh", str(args.launcher.resolve(strict=True))]
        command += [f"-addr=127.0.0.1:{port}", "-proxy=127.0.0.1:0", f"-data={root / 'data'}"]

        def request(path, data=None):
            body = None if data is None else json.dumps(data).encode()
            req = urllib.request.Request(base + path, data=body, headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=3) as response:
                return response.read()

        for attempt in range(2):
            with (root / f"run-{attempt}.log").open("w+") as log:
                process = subprocess.Popen(command, env=env, stdout=log, stderr=subprocess.STDOUT)
                try:
                    for _ in range(60):
                        if process.poll() is not None:
                            raise RuntimeError("Application exited before becoming ready")
                        try:
                            assert json.loads(request("/api/health"))["ok"]
                            break
                        except (OSError, ValueError, AssertionError):
                            time.sleep(0.5)
                    else:
                        raise RuntimeError("Health check timed out")
                    assert b"<html" in request("/chat/").lower(), "Embedded chat page is missing"
                    initialized = json.loads(request("/api/auth/status"))["initialized"]
                    assert initialized == bool(attempt), "SQLite state did not survive restart"
                    if not attempt:
                        request("/api/auth/init", {"password": secrets.token_urlsafe(24)})
                    assert (root / "state.db").is_file()
                except BaseException:
                    log.seek(0)
                    print(log.read())
                    raise
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                        raise RuntimeError("Launcher did not stop after termination")
        print("PASS: health, embedded UI, SQLite initialization, restart persistence, process shutdown")


if __name__ == "__main__":
    main()
