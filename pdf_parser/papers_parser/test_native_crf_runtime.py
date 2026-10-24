"""Validate the installed CRF native extension against an actual model file.

This tiny generated model checks serialization/loading on native runners;
production model distribution and extraction quality have separate gates.
"""
import tempfile
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


if __name__ == "__main__":
    unittest.main()
