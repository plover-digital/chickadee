#!/usr/bin/env python3
"""Local GitHub App manifest setup; no PAT, webhook listener, or third-party deps."""
import argparse
import base64
import html
import http.server
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def api(path, method="GET", token=None, body=None):
    headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28",
               "User-Agent": "chickadee-app-setup"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request("https://api.github.com" + path, data=None if body is None else json.dumps(body).encode(), method=method, headers=headers)
    try:
        with urllib.request.build_opener(NoRedirect).open(request, timeout=15) as response:
            data = response.read(1024 * 1024 + 1)
        if len(data) > 1024 * 1024:
            raise ValueError("GitHub response exceeds setup limit")
        return json.loads(data) if data else None
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"GitHub setup API returned HTTP {e.code}") from None
    except (urllib.error.URLError, json.JSONDecodeError):
        raise RuntimeError("GitHub setup API request failed") from None


def private_directory(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    s = path.lstat()
    if not stat.S_ISDIR(s.st_mode) or s.st_uid != os.getuid() or stat.S_IMODE(s.st_mode) & 0o077:
        raise ValueError("output must be an owned, private directory (chmod 0700)")


def save_new(path, data):
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())


def b64(data):
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def app_jwt(client_id, key):
    now = int(time.time())
    header = b64(b'{"alg":"RS256","typ":"JWT"}')
    claims = b64(json.dumps({"iat": now - 60, "exp": now + 540, "iss": client_id}).encode())
    message = (header + "." + claims).encode()
    result = subprocess.run(["openssl", "dgst", "-sha256", "-sign", str(key)],
                            input=message, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True)
    return message.decode() + "." + b64(result.stdout)


def manifest(org, name, callback):
    return {"name": name, "url": "https://github.com/plover-digital/chickadee",
            "description": "Single-host Chickadee ephemeral runner controller",
            "hook_attributes": {"url": "https://github.com/plover-digital/chickadee", "active": False},
            "redirect_url": callback, "public": False, "default_events": [],
            "default_permissions": {"organization_self_hosted_runners": "write"}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--org", required=True)
    parser.add_argument("--name")
    parser.add_argument("--port", type=int, default=18734)
    parser.add_argument("--output", type=Path, default=Path.home() / ".config/chickadee")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]{0,38}", args.org) or not 1024 <= args.port <= 65535:
        parser.error("invalid organization or local port")
    args.name = args.name or "chickadee-" + args.org
    # Never generate credentials inside the source checkout.
    output = args.output.expanduser().absolute()
    checkout = Path(__file__).resolve().parent.parent
    if output == checkout or checkout in output.parents:
        parser.error("choose a credential directory outside the checkout")
    private_directory(output)
    key = output / "app.pem"
    metadata = output / "app.json"
    if key.exists() or metadata.exists():
        parser.error("credentials already exist; use them or choose a fresh private directory")
    state = secrets.token_urlsafe(32)
    origin = f"http://127.0.0.1:{args.port}"
    app = None
    installed = False
    failure = False

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass  # Callback URLs contain one-use codes; never access-log them.

        def page(self, body, status=200):
            data = ("<!doctype html><meta charset=utf-8><title>Chickadee setup</title>" + body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("Referrer-Policy", "no-referrer")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            nonlocal app, failure
            if len(self.path) > 4096 or self.headers.get("Host") != f"127.0.0.1:{args.port}":
                self.page("Invalid setup request", 400)
                return
            url = urllib.parse.urlsplit(self.path)
            query = urllib.parse.parse_qs(url.query, max_num_fields=8)
            supplied = query.get("state", [""])
            if len(supplied) != 1 or not secrets.compare_digest(supplied[0], state):
                self.page("Invalid setup state", 403)
                return
            if url.path == "/start" and app is None:
                form = manifest(args.org, args.name, origin + "/callback")
                action = f"https://github.com/organizations/{args.org}/settings/apps/new?state={state}"
                self.page(f"<h1>Create your organization runner App</h1><p>Owner: {html.escape(args.org)}. "
                          "Self-hosted runners: read/write. Webhooks disabled. Private to the owner.</p>"
                          f'<form method="post" action="{html.escape(action, quote=True)}">'
                          f'<input type="hidden" name="manifest" value="{html.escape(json.dumps(form), quote=True)}">'
                          '<button>Create GitHub App on GitHub</button></form>')
            elif url.path == "/callback" and app is None:
                code = query.get("code", [""])
                if len(code) != 1 or not re.fullmatch(r"[A-Za-z0-9]{20,128}", code[0]):
                    self.page("Invalid manifest callback", 400)
                    return
                try:
                    response = api("/app-manifests/" + code[0] + "/conversions", "POST")
                    if (response["owner"]["login"].lower() != args.org.lower()
                            or response["permissions"].get("organization_self_hosted_runners") != "write"
                            or not re.fullmatch(r"[A-Za-z0-9-]+", response["slug"])
                            or "PRIVATE KEY" not in response["pem"]):
                        raise ValueError("created App does not match requested scope")
                    app = {"app_id": response["id"], "app_client_id": response["client_id"],
                           "app_slug": response["slug"], "github_url": "https://github.com/" + args.org,
                           "app_key_file": str(key)}
                    save_new(key, response["pem"])
                    save_new(metadata, json.dumps(app, indent=2) + "\n")
                    link = "https://github.com/apps/" + app["app_slug"] + "/installations/new"
                    self.page('<h1>App created; key saved privately</h1>'
                              f'<p><a href="{link}">Install the App on {html.escape(args.org)}</a>.</p>'
                              '<p>Choose only the repositories you intend to enable. Leave this setup process running; '
                              'it will detect the installation and save its ID automatically.</p>')
                    print("App created; waiting for its organization installation.", flush=True)
                except Exception:
                    failure = True
                    self.page("App setup failed. No credentials are displayed. Check local output files before retrying.", 500)
            else:
                self.page("Unknown or completed setup request", 404)

    server = http.server.HTTPServer(("127.0.0.1", args.port), Handler)
    server.timeout = 1
    print(f"Open {origin}/start?state={state} in a browser signed into the organization administrator account.", flush=True)
    print("If the browser is on another machine, forward this localhost port over SSH first.", flush=True)
    deadline = time.monotonic() + 1800
    next_poll = 0
    try:
        while time.monotonic() < deadline and not failure:
            server.handle_request()
            if app and time.monotonic() >= next_poll:
                next_poll = time.monotonic() + 5
                rows = api("/app/installations?per_page=100", token=app_jwt(app["app_client_id"], key))
                matches = [r for r in rows if r["account"]["login"].lower() == args.org.lower()
                           and r["app_id"] == app["app_id"] and not r.get("suspended_at")
                           and r["permissions"].get("organization_self_hosted_runners") == "write"]
                if len(matches) == 1:
                    app["app_installation_id"] = matches[0]["id"]
                    # Existing file was exclusively created by this process in an owned private directory.
                    with metadata.open("w") as f:
                        json.dump(app, f, indent=2)
                        f.write("\n")
                        f.flush()
                        os.fsync(f.fileno())
                    installed = True
                    print(f"Organization installation verified. Metadata: {metadata}; private key: {key}", flush=True)
                    break
    finally:
        server.server_close()
    if not installed:
        raise RuntimeError("setup unfinished; retained private files must be inspected before retrying")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.SubprocessError) as error:
        # Never print an API response, private key, signing output, or request URL.
        print("Chickadee App setup stopped: " + type(error).__name__, flush=True)
        raise SystemExit(1)
