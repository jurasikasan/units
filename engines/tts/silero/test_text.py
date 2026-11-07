import sys
import unittest

from text import MAX_CHARS, chunk


class ChunkTest(unittest.TestCase):
    def test_module_does_not_import_torch(self):
        # server.py loads models at import time; text.py must stay importable
        # on an image without torch (the gate's image has no torch at all).
        self.assertNotIn("torch", sys.modules)

    def test_max_chars_is_200(self):
        # 200 is the safe margin the task pins; the real Silero limit is a
        # separate unmeasured question and must not leak into this constant.
        self.assertEqual(MAX_CHARS, 200)

    def test_450_chars_no_punctuation_splits_at_word_boundaries(self):
        # Exactly 450 chars, no punctuation: 79 words of 4 chars plus one 55-char
        # word. The long word spans input offsets 195..208, i.e. it straddles
        # offset 200 — a blind fixed-position cut at 200 lands inside it.
        words = ["word"] * 80
        words[39] = "x" * 55
        text = " ".join(words)
        self.assertEqual(len(text), 450)
        # "no punctuation": every char is a word char or a space.
        self.assertFalse(any(not ch.isspace() and not ch.isalnum() for ch in text))

        chunks = chunk(text)
        self.assertTrue(chunks)

        # Every chunk is under the limit.
        for c in chunks:
            self.assertLessEqual(len(c), 200)

        # No chunk ends mid-word: the last token of each chunk is a whole word
        # of the input.
        input_words = set(text.split(" "))
        for c in chunks:
            self.assertIn(c.split(" ")[-1], input_words)

        # Chunks are contiguous pieces of the input separated by exactly one
        # space: every boundary sits on a space of the input, so no chunk ends
        # mid-word. Joining them with a single space reproduces the input.
        pos = 0
        for i, c in enumerate(chunks):
            if i:
                self.assertEqual(text[pos], " ")
                pos += 1
            self.assertEqual(text[pos:pos + len(c)], c)
            pos += len(c)
        self.assertEqual(pos, len(text))
        self.assertEqual(" ".join(chunks), text)


if __name__ == "__main__":
    unittest.main()
