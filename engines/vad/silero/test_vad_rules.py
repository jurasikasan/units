import unittest

import vad_rules


class TestValidateBodyLength(unittest.TestCase):
    def test_1024_bytes_is_valid(self):
        self.assertIsNone(vad_rules.validate_body_length(b"\x00" * 1024))

    def test_1023_bytes_odd_is_invalid(self):
        msg = vad_rules.validate_body_length(b"\x00" * 1023)
        self.assertIsNotNone(msg)

    def test_600_bytes_invalid_and_message_names_512_samples(self):
        msg = vad_rules.validate_body_length(b"\x00" * 600)
        self.assertIsNotNone(msg)
        self.assertIn("512", msg)

    def test_empty_body_invalid(self):
        self.assertIsNotNone(vad_rules.validate_body_length(b""))

    def test_2048_bytes_invalid(self):
        self.assertIsNotNone(vad_rules.validate_body_length(b"\x00" * 2048))


class TestSessionTtl(unittest.TestCase):
    def test_unset_defaults_to_300(self):
        self.assertEqual(vad_rules.session_ttl({}), 300)

    def test_numeric_string(self):
        self.assertEqual(vad_rules.session_ttl({"VAD_SESSION_TTL": "5"}), 5)

    def test_non_numeric_falls_back_to_default(self):
        self.assertEqual(vad_rules.session_ttl({"VAD_SESSION_TTL": "abc"}), 300)

    def test_zero_falls_back_to_default(self):
        self.assertEqual(vad_rules.session_ttl({"VAD_SESSION_TTL": "0"}), 300)

    def test_negative_falls_back_to_default(self):
        self.assertEqual(vad_rules.session_ttl({"VAD_SESSION_TTL": "-10"}), 300)


class TestSweepSeconds(unittest.TestCase):
    def test_unset_defaults_to_60(self):
        self.assertEqual(vad_rules.sweep_seconds({}), 60)

    def test_numeric_string(self):
        self.assertEqual(vad_rules.sweep_seconds({"VAD_SWEEP_SECONDS": "5"}), 5)

    def test_non_numeric_falls_back_to_default(self):
        self.assertEqual(vad_rules.sweep_seconds({"VAD_SWEEP_SECONDS": "abc"}), 60)

    def test_zero_falls_back_to_default(self):
        self.assertEqual(vad_rules.sweep_seconds({"VAD_SWEEP_SECONDS": "0"}), 60)


if __name__ == "__main__":
    unittest.main()
