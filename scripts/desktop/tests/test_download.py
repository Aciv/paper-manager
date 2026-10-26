"""Dependency download retries retain pinned checksums and atomic cache writes."""
import contextlib
import hashlib
import io
from pathlib import Path
import ssl
import tempfile
import unittest
import urllib.error
from unittest import mock

from desktop_bundle import download


class Response(io.BytesIO):
    def __init__(self, content, length=None):
        super().__init__(content)
        self.headers = {} if length is None else {"Content-Length": str(length)}


class InterruptedResponse(Response):
    def read(self, size=-1):
        if self.tell():
            raise ConnectionResetError("connection reset during transfer")
        return super().read(3)


class DownloadTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.cache = Path(self.temporary.name)
        self.content = b"complete pinned dependency"
        self.item = {"url": "https://example.org/dependency.zip",
                     "sha256": hashlib.sha256(self.content).hexdigest()}
        self.console = self.enterContext(contextlib.redirect_stdout(io.StringIO()))
        self.opener = self.enterContext(mock.patch("desktop_bundle.urllib.request.urlopen"))
        self.sleep = self.enterContext(mock.patch("desktop_bundle.time.sleep"))

    def assert_no_partial(self):
        self.assertFalse(list(self.cache.glob("*.part")))

    def test_windows_connection_timeout_retries_then_publishes_verified_cache(self):
        failure = urllib.error.URLError(TimeoutError(10060, "Windows connection timed out"))
        self.opener.side_effect = [failure, failure, Response(self.content)]
        result = download(self.item, self.cache)
        self.assertEqual(result.read_bytes(), self.content)
        self.assertEqual(self.opener.call_count, 3)
        self.assertEqual(self.sleep.call_args_list, [mock.call(2), mock.call(4)])
        self.assertIn("dependency.zip", self.console.getvalue())
        self.assertIn("(3/3)", self.console.getvalue())
        self.assertEqual(download(self.item, self.cache), result)
        self.assertEqual(self.opener.call_count, 3)
        self.assert_no_partial()

    def test_interrupted_transfer_restarts_without_appending_partial_bytes(self):
        self.opener.side_effect = [InterruptedResponse(self.content), Response(self.content)]
        self.assertEqual(download(self.item, self.cache).read_bytes(), self.content)
        self.assertEqual(self.opener.call_count, 2)
        self.assert_no_partial()

    def test_short_response_retries_even_when_its_hash_matches(self):
        self.opener.side_effect = [Response(self.content, len(self.content) + 1), Response(self.content)]
        self.assertEqual(download(self.item, self.cache).read_bytes(), self.content)
        self.assertEqual(self.opener.call_count, 2)
        self.assert_no_partial()

    def test_retry_budget_exhaustion_leaves_no_published_or_partial_cache(self):
        self.opener.side_effect = lambda *args, **kwargs: InterruptedResponse(self.content)
        with self.assertRaisesRegex(RuntimeError, "failed after 3 attempts"):
            download(self.item, self.cache)
        self.assertEqual(self.opener.call_count, 3)
        self.assertEqual(self.sleep.call_count, 2)
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_transient_http_errors_retry_but_permanent_errors_and_tls_do_not(self):
        for code in (408, 429, 500, 502, 503, 504, 403, 404):
            with self.subTest(code=code):
                self.opener.reset_mock()
                self.opener.side_effect = [urllib.error.HTTPError(self.item["url"], code, "failure", {}, None),
                                           Response(self.content)]
                if code in (403, 404):
                    with self.assertRaises(urllib.error.HTTPError):
                        download(self.item, self.cache)
                    self.assertEqual(self.opener.call_count, 1)
                else:
                    result = download(self.item, self.cache)
                    self.assertEqual(self.opener.call_count, 2)
                    result.unlink()
                self.assert_no_partial()
        self.opener.reset_mock()
        self.opener.side_effect = urllib.error.URLError(ssl.SSLCertVerificationError("untrusted certificate"))
        with self.assertRaises(urllib.error.URLError):
            download(self.item, self.cache)
        self.assertEqual(self.opener.call_count, 1)
        self.assert_no_partial()

    def test_bad_download_and_corrupt_cache_fail_without_retry_or_replacement(self):
        self.opener.return_value = Response(b"wrong dependency")
        with self.assertRaisesRegex(ValueError, "download hash mismatch"):
            download(self.item, self.cache)
        self.sleep.assert_not_called()
        self.assertEqual(list(self.cache.iterdir()), [])
        cached = self.cache / (self.item["sha256"] + "-dependency.zip")
        cached.write_bytes(b"corrupt existing cache")
        with self.assertRaisesRegex(ValueError, "cached dependency hash mismatch"):
            download(self.item, self.cache)
        self.assertEqual(cached.read_bytes(), b"corrupt existing cache")
        self.assertEqual(self.opener.call_count, 1)
        self.assert_no_partial()


if __name__ == "__main__":
    unittest.main()
