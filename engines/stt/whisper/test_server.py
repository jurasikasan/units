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


if __name__ == "__main__":
    unittest.main()
