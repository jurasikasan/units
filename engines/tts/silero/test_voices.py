import os
import sys
import unittest
from unittest import mock

from voices import RU_VOICES, build_voices


class BuildVoicesTest(unittest.TestCase):
    def test_module_does_not_import_torch(self):
        # The gate's image has no torch at all; server.py is not importable
        # there, which is exactly why the voice table lives in voices.py.
        self.assertNotIn("torch", sys.modules)

    def test_ru_voice_ids_are_the_five_pinned_ones(self):
        # From the task, not read back out of build_voices(): the five RU
        # voices of v4_ru as they stood in server.py before this change.
        self.assertEqual(
            set(RU_VOICES), {"aidar", "baya", "kseniya", "xenia", "eugene"}
        )
        self.assertTrue(all(v["lang"] == "ru" for v in RU_VOICES.values()))

    def test_en0_gives_six_voices(self):
        # AC: build_voices('en_0') → six voices — the five RU ones plus the
        # English speaker, and that one is the en entry.
        voices = build_voices("en_0")
        self.assertEqual(len(voices), 6)
        self.assertEqual(voices["en_0"]["lang"], "en")
        self.assertEqual(set(voices), set(RU_VOICES) | {"en_0"})

    def test_aidar_collision_raises_naming_variable_and_voice(self):
        # AC: build_voices('aidar') → exception whose message names the
        # variable and the voice, so the operator sees why the start failed.
        with self.assertRaises(Exception) as cm:
            build_voices("aidar")
        msg = str(cm.exception)
        self.assertIn("SILERO_SPEAKER_EN", msg)
        self.assertIn("aidar", msg)

    def test_silero_gender_en_sets_the_english_gender(self):
        # AC: SILERO_GENDER_EN=male → the English voice's gender is "male",
        # in the same "male"/"female" values the Russian voices use.
        with mock.patch.dict(os.environ, {"SILERO_GENDER_EN": "male"}):
            voices = build_voices("en_0")
        self.assertEqual(voices["en_0"]["gender"], "male")
        self.assertEqual(
            set(v["gender"] for v in RU_VOICES.values()), {"male", "female"}
        )

    def test_silero_gender_en_unset_leaves_english_gender_empty(self):
        # AC: variable unset → the English entry's gender is "" (as before).
        env = dict(os.environ)
        env.pop("SILERO_GENDER_EN", None)
        with mock.patch.dict(os.environ, env, clear=True):
            voices = build_voices("en_0")
        self.assertEqual(voices["en_0"]["gender"], "")

    def test_every_ru_id_collides(self):
        for voice_id in sorted(RU_VOICES):
            with self.assertRaises(ValueError, msg=voice_id):
                build_voices(voice_id)


if __name__ == "__main__":
    unittest.main()
