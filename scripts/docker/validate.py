#!/usr/bin/env python3
"""Build and exercise an isolated release Compose stack without publishing."""
from __future__ import annotations

import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ROOT / "packaging/docker/compose.yaml"
BUILD = ROOT / "packaging/docker/compose.build.yaml"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def free_port(excluded: set[int]) -> int:
    for _ in range(30):
        with socket.socket() as connection:
            connection.bind(("127.0.0.1", 0))
            port = connection.getsockname()[1]
        if port not in excluded:
            return port
    raise RuntimeError("could not select a test port")


def request(url: str, method="GET", body=None, headers=None):
    call = urllib.request.Request(url, data=body, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(call, timeout=15) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def fixture_pdf() -> bytes:
    text = b"BT /F1 20 Tf 72 720 Td (Docker release probe) Tj 0 -40 Td (Methods) Tj ET"
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>",
               b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
               b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R "
               b"/Resources << /Font << /F1 5 0 R >> >> >>",
               b"<< /Length " + str(len(text)).encode() + b" >>\nstream\n" + text + b"\nendstream",
               b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    result = bytearray(b"%PDF-1.4\n")
    offsets = [0]
    for index, data in enumerate(objects, 1):
        offsets.append(len(result))
        result.extend(f"{index} 0 obj\n".encode() + data + b"\nendobj\n")
    start = len(result)
    result.extend(f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode())
    for offset in offsets[1:]:
        result.extend(f"{offset:010d} 00000 n \n".encode())
    result.extend(f"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n{start}\n%%EOF\n".encode())
    return bytes(result)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--skip-build", action="store_true")
    parser.add_argument("--image-prefix", help="local test image prefix; images are never pushed")
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    project = "pm-docker-" + uuid.uuid4().hex[:10]
    prefix = args.image_prefix or project
    web_port = free_port(set())
    mcp_port = free_port({web_port})
    report = {"status": "failed", "project": project, "image_prefix": prefix,
              "architecture": "linux/amd64", "steps": []}
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    report["source_revision"] = revision
    report["working_tree_changes"] = bool(subprocess.check_output(
        ["git", "status", "--porcelain"], cwd=ROOT, text=True,
    ).strip())
    version = json.loads((ROOT / "packaging/desktop/product.json").read_text())["version"]
    token = secrets.token_hex(32)
    env_file = output / (project + ".env")
    data = output / (project + "-data")
    environment = {**os.environ, "POSTGRES_PASSWORD": secrets.token_hex(24), "MCP_TOKEN": token,
                   "WEB_PORT": str(web_port), "MCP_PORT": str(mcp_port),
                   "PAPER_MANAGER_IMAGE_PREFIX": prefix, "PAPER_MANAGER_VERSION": version,
                   "PAPER_MANAGER_REVISION": revision, "PAPER_MANAGER_DATA_DIR": str(data),
                   "PARSER_LAYOUT_THREADS": "2", "SEMANTIC_SCHOLAR_API_KEY": "",
                   "OPENALEX_API_KEY": "", "CROSSREF_MAILTO": ""}
    keys = ["POSTGRES_PASSWORD", "MCP_TOKEN", "WEB_PORT", "MCP_PORT", "PAPER_MANAGER_IMAGE_PREFIX",
            "PAPER_MANAGER_VERSION", "PAPER_MANAGER_REVISION", "PAPER_MANAGER_DATA_DIR",
            "PARSER_LAYOUT_THREADS", "SEMANTIC_SCHOLAR_API_KEY", "OPENALEX_API_KEY", "CROSSREF_MAILTO"]
    env_file.write_text("# Private validation instance. Do not publish.\n" + "".join(
        f"{key}={json.dumps(environment[key])}\n" for key in keys
    ))
    env_file.chmod(0o600)
    compose = ["docker", "compose", "--project-name", project, "--env-file", str(env_file),
               "-f", str(COMPOSE)]
    with ExitStack() as stack:
        log = stack.enter_context((output / "compose.log").open("wb"))

        def command(arguments, *, capture=False):
            result = subprocess.run(arguments, env=environment, cwd=ROOT, check=True,
                                    stdout=subprocess.PIPE if capture else log,
                                    stderr=log, text=capture)
            return result.stdout

        try:
            configuration = json.loads(command(compose + ["config", "--format", "json"], capture=True))
            services = configuration["services"]
            require(not any("build" in service for service in services.values()), "release Compose builds source")
            for name in ("postgres", "redis", "parser"):
                require(not services[name].get("ports"), f"{name} unexpectedly exposed on host")
            for name in ("api", "web"):
                require(all(port["host_ip"] == "127.0.0.1" for port in services[name]["ports"]),
                        f"{name} is not restricted to loopback")
            report["steps"].append("image-only Compose, private infrastructure, loopback HTTP/MCP")
            if not args.skip_build:
                command(compose + ["-f", str(BUILD), "build", "api", "parser", "web"])
            report["images"] = {}
            for name in ("api", "parser", "web"):
                image = f"{prefix}-{name}:{version}"
                metadata = json.loads(command(["docker", "image", "inspect", image], capture=True))[0]
                actual = metadata["Os"] + "/" + metadata["Architecture"]
                require(actual == "linux/amd64", f"not a validated amd64 image: {image}: {actual}")
                forbidden = {"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy",
                             "all_proxy", "DATABASE_URL", "MCP_TOKEN", "POSTGRES_PASSWORD"}
                baked = {value.split("=", 1)[0] for value in metadata["Config"].get("Env", [])}
                require(not baked.intersection(forbidden), f"build settings persisted in {image}")
                report["images"][name] = {"id": metadata["Id"], "architecture": actual,
                                        "bytes": metadata["Size"], "tag": image}
            command(compose + ["up", "-d", "--no-build", "--wait", "--wait-timeout", "180"])
            base = f"http://127.0.0.1:{web_port}"
            require(request(base + "/health") == (200, b'{"status":"ok"}'), "API health through web failed")
            code, html = request(base)
            require(code == 200 and b"<html" in html, "production frontend missing")
            mcp = f"http://127.0.0.1:{mcp_port}/mcp"
            body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                "protocolVersion": "2025-03-26", "capabilities": {},
                "clientInfo": {"name": "docker-probe", "version": "1"}}}).encode()
            headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
            require(request(mcp, "POST", body, headers)[0] == 401, "MCP accepted missing token")
            code, payload = request(mcp, "POST", body, {**headers, "Authorization": "Bearer " + token})
            require(code == 200 and b"serverInfo" in payload, "authenticated MCP initialization failed")
            report["steps"].append("production frontend, API through proxy and authenticated MCP")
            boundary = "pm-docker-probe"
            body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="probe.pdf"\r\n'
                    'Content-Type: application/pdf\r\n\r\n').encode()
            body += fixture_pdf() + f"\r\n--{boundary}--\r\n".encode()
            code, payload = request(base + "/api/document/upload", "POST", body,
                                    {"Content-Type": f"multipart/form-data; boundary={boundary}"})
            require(code == 201, f"PDF upload returned {code}")
            document = json.loads(payload)["document"]["id"]
            markdown_url = base + f"/api/document/{document}/markdown"
            deadline = time.monotonic() + 180
            while time.monotonic() < deadline:
                code, markdown = request(markdown_url)
                if code == 200 and b"Docker release probe" in markdown:
                    break
                time.sleep(1)
            else:
                raise RuntimeError("real PDF task did not yield readable Markdown")
            code, blocks = request(base + f"/api/document/{document}/blocks")
            require(code == 200 and json.loads(blocks)["items"], "PDF blocks were not persisted")
            note_url = base + f"/api/document/{document}/reading-note"
            body = json.dumps({"markdown": "# Summary\nDocker reading note.\n## Methods\nPersistence probe.",
                               "expectedRevision": 0}).encode()
            require(request(note_url, "PUT", body, {"Content-Type": "application/json"})[0] == 200,
                    "note import failed")
            report["document_id"] = document
            report["steps"].append("real PDF upload, Asynq, parser shared worker, Markdown/blocks and note files")
            command(compose + ["exec", "-T", "parser", "python", "/app/verify_parser.py"])
            command(compose + ["down", "--timeout", "75"])
            command(compose + ["up", "-d", "--no-build", "--wait", "--wait-timeout", "180"])
            require(request(markdown_url)[1] == markdown, "Markdown changed after container recreation")
            code, note = request(note_url)
            require(code == 200 and b"Methods" in note, "note directory lost after recreation")
            code, note_body = request(note_url + "/export")
            require(code == 200 and b"Persistence probe" in note_body, "note body lost after recreation")
            for directory in ("postgres", "redis", "uploads/notes", "recognition-output", "runtime"):
                require((data / directory).is_dir(), f"persistent path missing: {directory}")
            report["steps"].append("container recreation preserves database, notes, Markdown and shared storage")
            report["status"] = "passed"
        except Exception as error:
            report["error"] = f"{type(error).__name__}: {error}"
        finally:
            subprocess.run(compose + ["logs", "--no-color"], env=environment,
                           stdout=log, stderr=log, cwd=ROOT, check=False)
            result = subprocess.run(compose + ["down", "--timeout", "75"], env=environment,
                                    stdout=log, stderr=log, cwd=ROOT, check=False)
            if result.returncode:
                report["status"] = "failed"
                report["cleanup_error"] = "could not stop this validation project; see compose.log"
    (output / "validation.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2), flush=True)
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
