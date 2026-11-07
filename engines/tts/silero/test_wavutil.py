import io
import sys
import unittest
import wave

from wavutil import concat_wavs


def _wav(frames: int, framerate: int = 48000, nchannels: int = 1, sampwidth: int = 2) -> bytes:
    # 48000 Hz / 1 ch / 16-bit is the engine's fixed format (server.py
    # SAMPLE_RATE, PCM_16); the other arguments are how the task asks to
    # deviate from it.
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(nchannels)
        w.setsampwidth(sampwidth)
        w.setframerate(framerate)
        w.writeframes(b"\x00\x01" * frames)
    return buf.getvalue()


class ConcatWavsTest(unittest.TestCase):
    def test_module_does_not_import_torch(self):
        # server.py loads models at import time; wavutil.py must stay
        # importable on the gate's image, which has no torch at all.
        self.assertNotIn("torch", sys.modules)

    def test_mismatched_framerate_raises_valueerror(self):
        # AC: two WAVs with different rates -> ValueError. (Old code
        # silently reused the first chunk's rate and mis-timed the second.)
        with self.assertRaises(ValueError):
            concat_wavs([_wav(100, 48000), _wav(100, 16000)])

    def test_mismatched_nchannels_raises_valueerror(self):
        with self.assertRaises(ValueError):
            concat_wavs([_wav(100, nchannels=1), _wav(100, nchannels=2)])

    def test_mismatched_sampwidth_raises_valueerror(self):
        with self.assertRaises(ValueError):
            concat_wavs([_wav(100, sampwidth=2), _wav(100, sampwidth=1)])

    def test_identical_chunks_concat_with_sum_of_frames(self):
        # AC: two identical WAVs -> one WAV with the sum of frames, in order.
        out = concat_wavs([_wav(100), _wav(37)])
        with wave.open(io.BytesIO(out)) as w:
            self.assertEqual(w.getframerate(), 48000)
            self.assertEqual(w.getnchannels(), 1)
            self.assertEqual(w.getsampwidth(), 2)
            self.assertEqual(w.getnframes(), 137)
            self.assertEqual(w.readframes(137), b"\x00\x01" * 137)

    def test_single_chunk_returned_as_is(self):
        a = _wav(50)
        self.assertEqual(concat_wavs([a]), a)


if __name__ == "__main__":
    unittest.main()
