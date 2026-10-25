"""Validate the installed CRF native extension against an actual model file.

This tiny generated model checks serialization/loading on native runners;
production model distribution and extraction quality have separate gates.
"""
import json
import shutil
import tempfile
from unittest.mock import Mock
import unittest
from pathlib import Path

import pycrfsuite


class NativeCRFRuntimeTest(unittest.TestCase):
    def test_train_save_reload_and_infer(self):
        with tempfile.TemporaryDirectory(prefix="pm-crf-runtime-") as temporary:
            model = Path(temporary) / "probe.crfsuite"
            trainer = pycrfsuite.Trainer(verbose=False)
            for _ in range(4):
                trainer.append([{"token": "paper"}], ["title"])
                trainer.append([{"token": "alice"}], ["author"])
            trainer.set_params({"c1": 0, "c2": .01, "max_iterations": 10})
            trainer.train(str(model))
            tagger = pycrfsuite.Tagger()
            try:
                tagger.open(str(model))
                self.assertEqual(set(tagger.labels()), {"title", "author"})
                self.assertEqual(tagger.tag([{"token": "paper"}]), ["title"])
                self.assertEqual(tagger.tag([{"token": "alice"}]), ["author"])
            finally:
                tagger.close()

    def test_unicode_model_directory_predicts_same_labels(self):
        from papers_parser.crf_engine import _CRF

        with tempfile.TemporaryDirectory(prefix="pm-crf-unicode-") as temporary:
            root = Path(temporary)
            source = root / "ascii.crfsuite"
            trainer = pycrfsuite.Trainer(verbose=False)
            for _ in range(4):
                trainer.append([{"token": "paper"}], ["title"])
                trainer.append([{"token": "alice"}], ["author"])
            trainer.set_params({"c1": 0, "c2": .01, "max_iterations": 10})
            trainer.train(str(source))
            target = root / "安装目录 阅读 with spaces" / "model.crfsuite"
            target.parent.mkdir()
            shutil.copy2(source, target)
            manifest = target.with_suffix(".json")
            manifest.write_text(json.dumps({"schema_version": "paper-crf-artifact/v1",
                                            "feature_version": "probe/v1"}), encoding="utf-8")
            model = _CRF(target, "probe/v1", manifest)
            try:
                labels, probabilities = model.predict([{"token": "paper"}])
                self.assertEqual(labels, ["title"])
                self.assertGreater(probabilities[0], 0.5)
            finally:
                model._tagger.close()

    def test_memory_buffer_survives_temporary_allocations(self):
        from papers_parser.crf_engine import _open_crf_model

        with tempfile.TemporaryDirectory(prefix="pm-crf-memory-") as temporary:
            root = Path(temporary)
            source = root / "ascii.crfsuite"
            trainer = pycrfsuite.Trainer(verbose=False)
            trainer.append([{"word": "paper"}], ["title"])
            trainer.set_params({"max_iterations": 2})
            trainer.train(str(source))
            model = root / "模型.crfsuite"
            shutil.copy2(source, model)
            real = pycrfsuite.Tagger()
            proxy = Mock()
            proxy.open.side_effect = FileNotFoundError("ANSI path failure")
            proxy.open_inmemory.side_effect = real.open_inmemory
            retained = _open_crf_model(proxy, model, platform_name="nt")
            try:
                self.assertIsInstance(retained, bytes)
                for _ in range(50):
                    scratch = bytearray(model.stat().st_size)
                    self.assertEqual(real.tag([{"word": "paper"}]), ["title"])
                    self.assertGreater(len(scratch), 0)
            finally:
                real.close()

    def test_memory_loader_only_applies_to_windows_unicode_paths(self):
        from papers_parser.crf_engine import _open_crf_model

        with tempfile.TemporaryDirectory() as temporary:
            model = Path(temporary) / "模型.crfsuite"
            model.write_bytes(b"same immutable model")
            tagger = Mock()
            tagger.open.side_effect = FileNotFoundError("native ANSI fopen")
            retained = _open_crf_model(tagger, model, platform_name="nt")
            self.assertEqual(retained, b"same immutable model")
            tagger.open.assert_not_called()
            tagger.open_inmemory.assert_called_once_with(b"same immutable model")
            with self.assertRaises(FileNotFoundError):
                _open_crf_model(tagger, model, platform_name="posix")
            ascii_model = model.with_name("ascii.crfsuite")
            ascii_model.write_bytes(b"bad")
            with self.assertRaises(FileNotFoundError):
                _open_crf_model(tagger, ascii_model, platform_name="nt")
            # Corrupt models are not converted into successful loads.
            tagger.open_inmemory.side_effect = ValueError("invalid model")
            with self.assertRaises(ValueError):
                _open_crf_model(tagger, model, platform_name="nt")


if __name__ == "__main__":
    unittest.main()
