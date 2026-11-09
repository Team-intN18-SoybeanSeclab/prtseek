"""Check a Docker image with an isolated volume, including container replacement."""
import argparse
import json
import secrets
import socket
import subprocess
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    parser.add_argument("--host-network", action="store_true")
    args = parser.parse_args()
    name = "prtseek-smoke-" + uuid.uuid4().hex[:12]
    volume = name + "-data"
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]

    def docker(*argv, check=True):
        return subprocess.run(["docker", *argv], check=check, capture_output=True, text=True)

    def request(path, data=None):
        req = urllib.request.Request(f"http://127.0.0.1:{port}" + path,
                                     data=None if data is None else json.dumps(data).encode(),
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=3) as response:
            return response.read()

    password = secrets.token_urlsafe(24)
    try:
        docker("volume", "create", volume)
        for attempt in range(2):
            network = ["--network", "host"] if args.host_network else ["-p", f"127.0.0.1:{port}:8787"]
            address = f"127.0.0.1:{port}" if args.host_network else ":8787"
            docker("run", "-d", "--name", name, *network, "-v", f"{volume}:/app/data",
                   args.image, "-addr", address, "-proxy", "127.0.0.1:0")
            for _ in range(60):
                try:
                    assert json.loads(request("/api/health"))["ok"]
                    break
                except (OSError, ValueError, AssertionError):
                    time.sleep(0.5)
            else:
                raise RuntimeError("Container health check timed out")
            assert b"<html" in request("/chat/").lower()
            assert json.loads(request("/api/auth/status"))["initialized"] == bool(attempt)
            if not attempt:
                request("/api/auth/init", {"password": password})
            assert json.loads(request("/api/auth/login", {"username": "prtseek", "password": password}))["token"]
            docker("stop", "--timeout", "15", name)
            assert docker("inspect", "--format", "{{.State.ExitCode}}", name).stdout.strip() == "0"
            docker("rm", name)
    except BaseException:
        print(docker("logs", name, check=False).stdout)
        raise
    finally:
        docker("rm", "-f", name, check=False)
        docker("volume", "rm", volume, check=False)
    print("PASS: container health, embedded UI, login, volume persistence after replacement, graceful shutdown")


if __name__ == "__main__":
    main()
