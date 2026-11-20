import io
import threading
import time
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


class CollectTest(unittest.TestCase):
    # UNIT-1: decoding happens inside next() of faster-whisper's generator, so
    # a caller gone must stop the pulling, not only the reading of the result.
    def _gen(self, n, pulled):
        try:
            for i in range(n):
                pulled.append(i)
                yield i
        finally:
            pulled.append("closed")

    def test_all_segments_when_the_caller_stays(self):
        pulled = []
        segs, cancelled = stt_rules.collect(self._gen(3, pulled), lambda: False)
        self.assertEqual((segs, cancelled), ([0, 1, 2], False))

    def test_stops_pulling_once_the_caller_is_gone(self):
        pulled = []
        asked = iter([False, False, True])
        segs, cancelled = stt_rules.collect(self._gen(10, pulled), lambda: next(asked))
        self.assertEqual((segs, cancelled), ([0, 1], True))
        self.assertEqual(pulled, [0, 1, "closed"])

    def test_gone_before_the_first_segment_decodes_nothing(self):
        pulled = []
        segs, cancelled = stt_rules.collect(self._gen(10, pulled), lambda: True)
        self.assertEqual((segs, cancelled), ([], True))
        self.assertNotIn(0, pulled)


class WaitTurnTest(unittest.TestCase):
    def test_free_slot_is_taken(self):
        slot = threading.Semaphore(1)
        self.assertTrue(stt_rules.wait_turn(slot, lambda: False))
        self.assertFalse(slot.acquire(blocking=False))

    def test_caller_gone_while_waiting_gives_up_without_the_slot(self):
        slot = threading.Semaphore(1)
        slot.acquire()
        t0 = time.monotonic()
        self.assertFalse(stt_rules.wait_turn(slot, lambda: True, poll_s=0.01))
        self.assertLess(time.monotonic() - t0, 1)
        slot.release()
        self.assertTrue(slot.acquire(blocking=False))
        self.assertFalse(slot.acquire(blocking=False))

    def test_caller_gone_just_as_the_slot_frees_does_not_keep_it(self):
        slot = threading.Semaphore(1)
        self.assertFalse(stt_rules.wait_turn(slot, lambda: True))
        self.assertTrue(slot.acquire(blocking=False))

    def test_waits_for_a_slot_released_by_another_decode(self):
        slot = threading.Semaphore(1)
        slot.acquire()
        threading.Timer(0.05, slot.release).start()
        self.assertTrue(stt_rules.wait_turn(slot, lambda: False, poll_s=0.01))


if __name__ == "__main__":
    unittest.main()
