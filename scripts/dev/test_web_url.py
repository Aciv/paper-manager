"""Check advertised dev URLs without starting Go, databases, or Vite."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class DevWebURLTests(unittest.TestCase):
    def run_app(self, host, port, explicit=""):
        with tempfile.TemporaryDirectory() as directory:
            fake_go = Path(directory) / "go"
            fake_go.write_text('#!/bin/sh\nprintf "%s" "$WEB_BASE_URL"\n')
            fake_go.chmod(0o755)
            env = os.environ.copy()
            env.update({
                "PATH": directory + os.pathsep + env["PATH"],
                "DEV_AUTO_RELOAD": "false",
                "WEB_BASE_URL": explicit,
                "VITE_HOST": host,
                "VITE_PORT": port,
            })
            return subprocess.check_output(
                ["bash", str(ROOT / "scripts/dev/dev.sh"), "app"],
                env=env, text=True, stderr=subprocess.DEVNULL,
            )

    def test_custom_port(self):
        self.assertEqual(self.run_app("127.0.0.1", "53421"), "http://127.0.0.1:53421/")

    def test_wildcard_and_ipv6(self):
        self.assertEqual(self.run_app("0.0.0.0", "53422"), "http://127.0.0.1:53422/")
        self.assertEqual(self.run_app("::", "53423"), "http://[::1]:53423/")

    def test_explicit_proxy_address(self):
        self.assertEqual(
            self.run_app("127.0.0.1", "53421", "https://papers.example/app/"),
            "https://papers.example/app/",
        )


if __name__ == "__main__":
    unittest.main()
