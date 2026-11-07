"""a model that fails to load must end up in `failed`, not vanish.

Standard library only, on purpose: the gate image has no fastapi, piper or
torch, so server.py itself cannot be imported here. load_voices() is the
policy under test; the voice constructor is injected, standing in for
server.Voice (read the .json config, then load the model, which is the step
a corrupt .onnx makes fail).

Expected values are the task's acceptance criteria, not this code's shape:
  * broken model → failed contains {file name: error text}
  * all models load → failed is empty
"""

import contextlib
import io
import json
import os
import re
import tempfile
import unittest

import loader


class CorruptModelVoice:
    """What server.Voice does, minus piper: read the .json config, then
    load the model. Loading a model file it does not recognise raises —
    the same failure a corrupt .onnx produces for the real voice."""

    GOOD = b"onnx-magic-ok"

    def __init__(self, path: str):
        with open(path + ".json") as f:
            json.load(f)
        with open(path, "rb") as f:
            if self.GOOD not in f.read():
                raise ValueError("corrupt model: unrecognised onnx header")
        self.path = path


class LoadVoicesTest(unittest.TestCase):
    def _write_model(self, d: str, name: str, good: bool) -> None:
        with open(os.path.join(d, name), "wb") as f:
            f.write(CorruptModelVoice.GOOD if good else b"\x00\x01garbage")
        with open(os.path.join(d, name + ".json"), "w") as f:
            json.dump({"audio": {"sample_rate": 22050}}, f)

    def test_broken_model_is_reported_in_failed(self):
        with tempfile.TemporaryDirectory() as d:
            self._write_model(d, "ru_RU-denis-medium.onnx", good=True)
            self._write_model(d, "ru_RU-borka-high.onnx", good=False)
            voices, failed = loader.load_voices(d, CorruptModelVoice)
        # the broken voice is absent from voices, present in failed…
        self.assertEqual(list(voices), ["denis"])
        # …keyed by file name, with the error text as the value
        self.assertEqual(list(failed), ["ru_RU-borka-high.onnx"])
        self.assertIn("corrupt model", failed["ru_RU-borka-high.onnx"])

    def test_all_loaded_failed_is_empty(self):
        with tempfile.TemporaryDirectory() as d:
            self._write_model(d, "ru_RU-denis-medium.onnx", good=True)
            self._write_model(d, "ru_RU-irina-low.onnx", good=True)
            voices, failed = loader.load_voices(d, CorruptModelVoice)
        self.assertEqual(failed, {})
        self.assertEqual(list(voices), ["denis", "irina"])

    def test_missing_models_dir_is_empty_not_an_error(self):
        voices, failed = loader.load_voices(
            os.path.join(tempfile.gettempdir(), "no-such-piper-dir"),
            CorruptModelVoice)
        self.assertEqual(voices, {})
        self.assertEqual(failed, {})


# two .onnx files that map to one voice_id used to overwrite each
# other silently — the last file in sorted order won with no trace in the
# log. Expected values are the task's acceptance criteria, not this code's
# shape:
#   * ru_RU-denis-low.onnx + ru_RU-denis-medium.onnx → exactly one warning
#     line carrying BOTH file names and the winner; the winner is the last
#     in sorted order (ru_RU-denis-medium.onnx, per the task)
#   * two files with different ids → no warning
class FakeVoice:
    """Stands in for server.Voice; the .onnx files it loads are empty and
    unreadable as models, so nothing about its behaviour is under test —
    only the loader's collision policy."""

    def __init__(self, path: str):
        self.path = path


class CollisionWarningTest(unittest.TestCase):
    def _load(self, d: str, names):
        for name in names:
            with open(os.path.join(d, name), "wb"):
                pass  # empty file — FakeVoice never reads it
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            voices, failed = loader.load_voices(d, FakeVoice)
        return voices, failed, err.getvalue()

    def test_duplicate_voice_id_warns_with_both_names_and_winner(self):
        with tempfile.TemporaryDirectory() as d:
            voices, failed, log = self._load(
                d, ["ru_RU-denis-low.onnx", "ru_RU-denis-medium.onnx"])
        # behaviour is unchanged: last in sorted order is served…
        self.assertEqual(list(voices), ["denis"])
        self.assertEqual(
            os.path.basename(voices["denis"].path), "ru_RU-denis-medium.onnx")
        self.assertEqual(failed, {})
        # …and it is no longer silent: one line, both file names…
        warnings = [l for l in log.splitlines() if "voice_id collision" in l]
        self.assertEqual(len(warnings), 1)
        line = warnings[0]
        self.assertIn("ru_RU-denis-low.onnx", line)
        self.assertIn("ru_RU-denis-medium.onnx", line)
        # …and a name designated as the winner, which is the task's
        # expected value: the last file in sorted order.
        winners = re.findall(r"serving '([^']+)'", line)
        self.assertEqual(winners, ["ru_RU-denis-medium.onnx"])

    def test_distinct_ids_do_not_warn(self):
        with tempfile.TemporaryDirectory() as d:
            voices, failed, log = self._load(
                d, ["ru_RU-denis-medium.onnx", "ru_RU-irina-low.onnx"])
        self.assertEqual(list(voices), ["denis", "irina"])
        self.assertEqual(log, "")


# gender comes from a voices.json beside the models; the engine's
# table is the fallback for the baked-in four. Expected values are the
# task's acceptance criteria, not this code's shape:
#   * id NOT in the baked-in four, with a voices.json entry → that gender
#   * baked-in id, no file entry → the table's value
#   * no voices.json at all → the table's value / "" for everything else
GENDERS = {"denis": "male", "dmitri": "male", "irina": "female", "ruslan": "male"}


# a file name without an underscore carries no language. Expected
# values are the task's acceptance criteria, not this code's shape:
#   * ru_RU-denis-medium.onnx → "ru"
#   * nora.onnx → ""
class LangOfTest(unittest.TestCase):
    def test_language_prefix_of_standard_name(self):
        self.assertEqual(loader.lang_of("ru_RU-denis-medium.onnx"), "ru")

    def test_name_without_underscore_has_no_language(self):
        self.assertEqual(loader.lang_of("nora.onnx"), "")


class GenderTest(unittest.TestCase):
    def _write_meta(self, d: str, meta) -> dict:
        with open(os.path.join(d, "voices.json"), "w") as f:
            if isinstance(meta, str):
                f.write(meta)
            else:
                json.dump(meta, f)
        return loader.load_voice_meta(d)

    def test_unbaked_voice_gender_from_file(self):
        with tempfile.TemporaryDirectory() as d:
            meta = self._write_meta(
                d, {"sveta": {"gender": "female", "langs": ["ru"]}})
        self.assertEqual(loader.resolve_gender("sveta", meta, GENDERS),
                         "female")

    def test_baked_voice_without_file_entry_falls_back_to_table(self):
        with tempfile.TemporaryDirectory() as d:
            meta = self._write_meta(d, {"sveta": {"gender": "female"}})
        self.assertEqual(loader.resolve_gender("denis", meta, GENDERS),
                         "male")

    def test_no_file_table_is_the_fallback(self):
        with tempfile.TemporaryDirectory() as d:
            meta = loader.load_voice_meta(d)
        self.assertEqual(meta, {})
        self.assertEqual(loader.resolve_gender("irina", meta, GENDERS),
                         "female")
        self.assertEqual(loader.resolve_gender("sveta", meta, GENDERS), "")

    def test_file_entry_wins_over_table(self):
        with tempfile.TemporaryDirectory() as d:
            meta = self._write_meta(d, {"irina": {"gender": "male"}})
        self.assertEqual(loader.resolve_gender("irina", meta, GENDERS),
                         "male")

    def test_malformed_file_is_fallback_not_a_crash(self):
        with tempfile.TemporaryDirectory() as d:
            meta = self._write_meta(d, "{ not json")
        self.assertEqual(meta, {})
        self.assertEqual(loader.resolve_gender("denis", meta, GENDERS),
                         "male")


if __name__ == "__main__":
    unittest.main()
