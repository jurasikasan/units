import re
import unittest


def _end_line():
    with open("server.py", encoding="utf-8") as f:
        src = f.read()
    m = re.search(r"\[transcribe\] END.*", src)
    assert m, "no END log line in server.py"
    return m.group(0)


class EndLogFormatTest(unittest.TestCase):
    def test_end_line_truncates_text_then_reprs(self):
        # Acceptance criterion: the END line uses {text[:80]!r}.
        self.assertIn("{text[:80]!r}", _end_line())
        self.assertNotIn("text!r:.80", _end_line())

    def test_long_text_is_logged_with_terminated_repr(self):
        # Truncating the repr ({text!r:.80}) leaves an unterminated quote;
        # truncating the text first must render exactly repr(text[:80]).
        m = re.search(r"→ (\{.*\})", _end_line())
        self.assertIsNotNone(m, "END line lost its → {…} fragment")
        text = "x" * 500
        rendered = eval(f"f\"{m.group(1)}\"", {}, {"text": text})
        self.assertEqual(rendered, repr(text[:80]))
        self.assertEqual(rendered, "'" + "x" * 80 + "'")


class SegmentLogTest(unittest.TestCase):
    # UNIT-13: a hallucinated sentence on a short clip is told apart only by
    # these numbers being read; each segment's must be in the log.
    def test_segment_line_carries_the_decoder_numbers(self):
        with open("server.py", encoding="utf-8") as f:
            src = f.read()
        m = re.search(r"\[transcribe\] SEG.*?\)\n", src, re.S)
        self.assertIsNotNone(m, "no SEG log line in server.py")
        for field in ("s.start", "s.end", "s.no_speech_prob", "s.avg_logprob", "s.compression_ratio"):
            self.assertIn(field, m.group(0))


if __name__ == "__main__":
    unittest.main()
