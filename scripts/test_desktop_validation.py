"""Focused tests for baseline gates and dependency inventory helpers."""
import copy
import contextlib
import importlib.util
import io
import json
import tempfile
import unittest
from unittest import mock
from pathlib import Path


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


benchmark = load("benchmark_parser_shared")
dependencies = load("check_desktop_dependencies")
postgres = load("check_private_postgres")
integration = load("run_desktop_integration")


class DesktopValidationTest(unittest.TestCase):
    def baseline(self):
        return {
            "fingerprint": {"host": "fixture", "jobs": 8},
            "cases": [{
                "concurrency": 4, "ready_seconds": 1.0,
                "peak_tree_rss": 500 << 20, "peak_parent_rss": 40 << 20,
                "idle_after_retirement": {"parent_rss": 40 << 20, "tree_rss": 40 << 20},
                "metrics": {"recognize": {"jobs_per_second": 4, "p95_seconds": 1}},
            }],
        }

    def test_small_noise_does_not_trip_gate(self):
        baseline = self.baseline()
        current = copy.deepcopy(baseline)
        current["cases"][0]["peak_parent_rss"] += 4 << 20
        self.assertEqual(benchmark.compare(current, baseline), [])

    def test_performance_and_memory_regressions_fail_independently(self):
        baseline = self.baseline()
        changes = [
            ("throughput", lambda case: case["metrics"]["recognize"].update(jobs_per_second=2)),
            ("p95", lambda case: case["metrics"]["recognize"].update(p95_seconds=2)),
            ("peak_tree_rss", lambda case: case.update(peak_tree_rss=700 << 20)),
            ("idle", lambda case: case["idle_after_retirement"].update(parent_rss=80 << 20)),
        ]
        for expected, change in changes:
            with self.subTest(expected=expected):
                current = copy.deepcopy(baseline)
                change(current["cases"][0])
                self.assertTrue(any(expected in failure for failure in benchmark.compare(current, baseline)))

    def test_different_host_or_load_is_not_a_valid_comparison(self):
        baseline = self.baseline()
        current = copy.deepcopy(baseline)
        current["fingerprint"]["host"] = "other"
        with self.assertRaises(ValueError):
            benchmark.compare(current, baseline)
        current = copy.deepcopy(baseline)
        current["cases"][0]["concurrency"] = 1
        with self.assertRaises(ValueError):
            benchmark.compare(current, baseline)

    def test_aggregate_uses_three_run_median_and_keeps_noise_range(self):
        reports = [self.baseline() for _ in range(3)]
        for report, latency in zip(reports, (1.0, 1.1, 3.0)):
            report["cases"].append(copy.deepcopy(report["cases"][0]))
            report["cases"][0]["concurrency"] = 1
            report["cases"][0]["metrics"]["recognize"]["p95_seconds"] = latency
        summary = benchmark.aggregate(reports)
        self.assertEqual(summary["cases"][0]["metrics"]["recognize"]["p95_seconds"], 1.1)
        self.assertEqual(summary["observed_ranges"]["1"]["recognize"]["p95_seconds"]["max"], 3.0)
        with self.assertRaises(ValueError):
            benchmark.aggregate(reports[:2])

    def test_streamed_digest_detects_modified_artifact(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "artifact"
            path.write_bytes(b"candidate")
            digest = dependencies.sha256(path)
            path.write_bytes(b"modified")
            self.assertNotEqual(dependencies.sha256(path), digest)

    def test_postgres_preflight_reports_failure_without_connecting(self):
        import json
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "result.json"
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(postgres.probe(Path(temporary) / "missing-bin", output), 1)
            report = json.loads(output.read_text())
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["steps"], [])

    def test_native_gate_requires_actual_pass_events_and_rejects_skip(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "events.jsonl"
            events = [{"Action": "pass", "Test": name} for name in integration.REQUIRED_TESTS]
            path.write_text("\n".join(json.dumps(event) for event in events))
            self.assertEqual(set(integration.verify_events(path)), integration.REQUIRED_TESTS)
            path.write_text("\n".join(json.dumps(event) for event in events[1:]))
            with self.assertRaises(RuntimeError):
                integration.verify_events(path)
            events.append({"Action": "skip", "Test": "TestSomeOtherDatabasePostgres"})
            path.write_text("\n".join(json.dumps(event) for event in events))
            with self.assertRaises(RuntimeError):
                integration.verify_events(path)

    def test_sampler_counts_helper_memory_without_duplicate_worker(self):
        parent = mock.Mock(pid=100)
        parent.memory_info.return_value.rss = 10
        worker, helper = mock.Mock(pid=101), mock.Mock(pid=102)
        for child, ppid, rss in ((worker, 100, 20), (helper, 101, 30)):
            child.cmdline.return_value = ["python", "-m", "papers_parser.worker", "--kind", "recognize"]
            child.ppid.return_value = ppid
            child.memory_info.return_value.rss = rss
            child.num_threads.return_value = 2
        parent.children.return_value = [worker, helper]
        observed = benchmark.snapshot(parent)
        self.assertEqual([item["pid"] for item in observed["workers"]], [101])
        self.assertEqual([item["pid"] for item in observed["helpers"]], [102])
        self.assertEqual(observed["tree_rss"], 60)

    def test_sampler_ignores_only_confirmed_zombies_after_access_denied(self):
        parent, helper = mock.Mock(pid=100), mock.Mock(pid=101)
        parent.memory_info.return_value.rss = 10
        parent.children.return_value = [helper]
        helper.cmdline.side_effect = benchmark.psutil.AccessDenied(pid=101)
        helper.is_running.return_value = True
        helper.status.return_value = benchmark.psutil.STATUS_ZOMBIE
        self.assertEqual(benchmark.snapshot(parent)["tree_rss"], 10)
        helper.status.return_value = benchmark.psutil.STATUS_RUNNING
        with self.assertRaises(benchmark.psutil.AccessDenied):
            benchmark.snapshot(parent)


if __name__ == "__main__":
    unittest.main()
