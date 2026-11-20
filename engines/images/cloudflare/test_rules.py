import base64
import json
import unittest

import rules
from rules import Refusal

# Schemas as Cloudflare's /ai/models/schema gave them on 2026-11-20, trimmed.
SCHNELL = {"input": {"properties": {"prompt": {}, "steps": {}}, "additionalProperties": False},
           "output": {"contentType": "application/json"}}
KLEIN = {"input": {"properties": {"multipart": {}}, "required": ["multipart"]}}
LUCID = {"input": {"properties": {"prompt": {}, "guidance": {}, "seed": {}, "height": {}, "width": {}}}}

JPEG = b"\xff\xd8\xff\xe0" + b"\0" * 16
PNG = b"\x89PNG\r\n\x1a\n" + b"\0" * 16


class Names(unittest.TestCase):
    def test_full_name_kept(self):
        self.assertEqual(rules.model_name("@cf/leonardo/lucid-origin", "d"), "@cf/leonardo/lucid-origin")

    def test_short_name_gets_cf(self):
        # LiteLLM's openai/black-forest-labs/flux-1-schnell hands on the rest.
        self.assertEqual(rules.model_name("black-forest-labs/flux-1-schnell", "d"),
                         "@cf/black-forest-labs/flux-1-schnell")

    def test_empty_is_default(self):
        self.assertEqual(rules.model_name("", "@cf/x/y"), "@cf/x/y")
        self.assertEqual(rules.model_name(None, "@cf/x/y"), "@cf/x/y")


class Params(unittest.TestCase):
    def test_size(self):
        self.assertEqual(rules.parse_size("768x512"), (768, 512))
        self.assertIsNone(rules.parse_size("auto"))
        self.assertIsNone(rules.parse_size(None))
        for bad in ("768", "axb", "10x10", "5000x512", 512):
            with self.assertRaises(Refusal) as c:
                rules.parse_size(bad)
            self.assertEqual(c.exception.param, "size")

    def test_n(self):
        self.assertEqual(rules.parse_n(None), 1)
        self.assertEqual(rules.parse_n(3), 3)
        for bad in (0, 5, "2", True):
            with self.assertRaises(Refusal):
                rules.parse_n(bad)

    def test_format(self):
        self.assertIsNone(rules.parse_format(None))
        self.assertEqual(rules.parse_format("jpg"), "jpeg")
        self.assertEqual(rules.parse_format("webp"), "webp")
        with self.assertRaises(Refusal):
            rules.parse_format("gif")

    def test_url_is_refused(self):
        rules.parse_response_format("b64_json")
        rules.parse_response_format(None)
        with self.assertRaises(Refusal) as c:
            rules.parse_response_format("url")
        self.assertEqual(c.exception.status, 400)

    def test_seed(self):
        self.assertEqual(rules.parse_seed(7), 7)
        for bad in (-1, "7", 1.5, True):
            with self.assertRaises(Refusal):
                rules.parse_seed(bad)


class Build(unittest.TestCase):
    def test_json_model_gets_only_what_its_schema_names(self):
        # flux-1-schnell refuses additional properties and has no size.
        kind, body = rules.build_request(SCHNELL, "a fox", (512, 512), 3)
        self.assertEqual((kind, body), ("json", {"prompt": "a fox"}))

    def test_json_model_with_size_and_seed(self):
        kind, body = rules.build_request(LUCID, "a fox", (768, 512), 3)
        self.assertEqual(body, {"prompt": "a fox", "width": 768, "height": 512, "seed": 3})

    def test_multipart_model(self):
        kind, fields = rules.build_request(KLEIN, "a fox", (768, 512), None, [PNG, JPEG])
        self.assertEqual(kind, "multipart")
        self.assertEqual(fields[:3], [("prompt", "a fox"), ("width", "768"), ("height", "512")])
        self.assertEqual([n for n, _ in fields[3:]], ["input_image_0", "input_image_1"])

    def test_images_to_a_model_that_takes_none(self):
        with self.assertRaises(Refusal) as c:
            rules.build_request(SCHNELL, "a fox", None, None, [PNG])
        self.assertEqual(c.exception.param, "image")

    def test_too_many_images(self):
        with self.assertRaises(Refusal):
            rules.build_request(KLEIN, "a fox", None, None, [PNG] * 5)

    def test_prompt_required(self):
        for bad in (None, "", "  ", 5):
            with self.assertRaises(Refusal) as c:
                rules.build_request(KLEIN, bad, None, None)
            self.assertEqual(c.exception.param, "prompt")


class Answer(unittest.TestCase):
    def test_json_envelope(self):
        body = json.dumps({"result": {"image": base64.b64encode(JPEG).decode()}}).encode()
        self.assertEqual(rules.extract_image("application/json", body), JPEG)

    def test_binary_image(self):
        self.assertEqual(rules.extract_image("image/jpeg", JPEG), JPEG)

    def test_no_image(self):
        for ctype, body in (("application/json", b'{"result":{}}'),
                            ("application/json", b'{"result":{"image":"%%%"}}'),
                            ("text/html", b"<html>")):
            with self.assertRaises(Refusal) as c:
                rules.extract_image(ctype, body)
            self.assertEqual(c.exception.status, 502)

    def test_sniff(self):
        self.assertEqual(rules.sniff_format(JPEG), "jpeg")
        self.assertEqual(rules.sniff_format(PNG), "png")
        self.assertEqual(rules.sniff_format(b"RIFF\0\0\0\0WEBPVP8 "), "webp")
        self.assertIsNone(rules.sniff_format(b"GIF89a"))


class Errors(unittest.TestCase):
    def err(self, status, message):
        return rules.upstream_error(status, json.dumps({"errors": [{"message": message}]}).encode())

    def test_unknown_model(self):
        # What /ai/models/schema answered for @cf/nope/nope.
        e = self.err(404, "Model schema not found")
        self.assertEqual((e.status, e.code), (404, "model_not_found"))

    def test_bad_input_stays_400_with_message(self):
        e = self.err(400, "AiError: Bad input: Additional properties '/width' not allowed")
        self.assertEqual(e.status, 400)
        self.assertIn("/width", e.message)

    def test_quota(self):
        self.assertEqual(self.err(429, "slow down").status, 429)
        self.assertEqual(self.err(400, "you have used up your daily free allocation of 10,000 neurons").status, 429)

    def test_bad_key_is_ours_not_the_callers(self):
        e = self.err(401, "Authentication error")
        self.assertEqual(e.status, 502)
        self.assertIn("key", e.message)

    def test_garbage(self):
        e = rules.upstream_error(503, b"<html>")
        self.assertEqual(e.status, 502)

    def test_error_body_is_openais_shape(self):
        b = Refusal(400, "size bad", "size").body()["error"]
        self.assertEqual((b["type"], b["param"], b["message"]), ("invalid_request_error", "size", "size bad"))


if __name__ == "__main__":
    unittest.main()
