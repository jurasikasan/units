import io
import unittest

import stt_rules


class MaxUploadTest(unittest.TestCase):
    def test_default_is_25_mib(self):
        self.assertEqual(stt_rules.max_upload_bytes({}), 25 * 1024 * 1024)

    def test_env_sets_it(self):
        self.assertEqual(stt_rules.max_upload_bytes({"STT_MAX_UPLOAD_BYTES": "1000"}), 1000)

    def test_garbage_and_nonpositive_fall_back(self):
        for raw in ("x", "0", "-5", ""):
            self.assertEqual(stt_rules.max_upload_bytes({"STT_MAX_UPLOAD_BYTES": raw}), 25 * 1024 * 1024)


class ReadCappedTest(unittest.TestCase):
    def test_at_limit_passes(self):
        self.assertEqual(stt_rules.read_capped(io.BytesIO(b"abcd"), 4), b"abcd")

    def test_one_over_is_refused(self):
        with self.assertRaises(stt_rules.TooLarge):
            stt_rules.read_capped(io.BytesIO(b"abcde"), 4)


class AudioFieldsTest(unittest.TestCase):
    def test_known_duration_gives_rtf(self):
        self.assertEqual(stt_rules.audio_fields(2.0, 1000), " audio=2.00s RTF=0.50x")

    def test_empty_clip_is_zero_not_missing(self):
        self.assertEqual(stt_rules.audio_fields(0.0, 120), " audio=0.00s")

    def test_unknown_duration_drops_fields(self):
        self.assertEqual(stt_rules.audio_fields(None, 120), "")


if __name__ == "__main__":
    unittest.main()
