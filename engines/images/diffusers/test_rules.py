import unittest

import rules
from rules import Refusal

# Flux2KleinPipeline.__call__'s names (diffusers 0.43), trimmed to what matters.
KLEIN = {"image", "prompt", "height", "width", "num_inference_steps", "guidance_scale",
         "num_images_per_prompt", "generator", "prompt_embeds"}
# A text-to-image pipeline with a negative prompt and no image (SDXL-like).
SDXL = {"prompt", "height", "width", "num_inference_steps", "guidance_scale",
        "negative_prompt", "num_images_per_prompt", "generator"}
# One picture per call, no size: the least a pipeline may take.
BARE = {"prompt", "num_inference_steps"}


class Params(unittest.TestCase):
    def test_size(self):
        self.assertEqual(rules.parse_size("1024x768"), (1024, 768))
        self.assertIsNone(rules.parse_size("auto"))
        self.assertIsNone(rules.parse_size(None))
        for bad in ("1024", "axb", "32x32", "8192x512", "1000x1000", 512):
            with self.assertRaises(Refusal) as c:
                rules.parse_size(bad)
            self.assertEqual(c.exception.param, "size")

    def test_numbers_from_json_and_from_a_form(self):
        self.assertEqual(rules.parse_n(None), 1)
        self.assertEqual(rules.parse_n("2"), 2)
        self.assertEqual(rules.parse_seed("7"), 7)
        self.assertEqual(rules.parse_steps(16), 16)
        self.assertEqual(rules.parse_guidance("3.5"), 3.5)
        for f, bad in ((rules.parse_n, 5), (rules.parse_n, True), (rules.parse_seed, -1),
                       (rules.parse_steps, 0), (rules.parse_steps, "x"), (rules.parse_guidance, "x"),
                       (rules.parse_guidance, 99)):
            with self.assertRaises(Refusal):
                f(bad)

    def test_format_defaults_to_png_as_openai(self):
        self.assertEqual(rules.parse_format(None), "png")
        self.assertEqual(rules.parse_format("jpg"), "jpeg")
        with self.assertRaises(Refusal):
            rules.parse_format("gif")

    def test_url_refused(self):
        rules.parse_response_format("b64_json")
        with self.assertRaises(Refusal):
            rules.parse_response_format("url")

    def test_prompt(self):
        for bad in (None, "", "  ", 3):
            with self.assertRaises(Refusal):
                rules.parse_prompt(bad)


class Call(unittest.TestCase):
    def test_flux_klein(self):
        kw = rules.call_kwargs(KLEIN, "a fox", (1024, 768), 2, None, None,
                               defaults={"steps": 4, "guidance": 1.0})
        self.assertEqual(kw, {"prompt": "a fox", "width": 1024, "height": 768, "num_images_per_prompt": 2,
                              "num_inference_steps": 4, "guidance_scale": 1.0})

    def test_request_beats_engine_defaults(self):
        kw = rules.call_kwargs(KLEIN, "a fox", None, 1, 8, 2.0, defaults={"steps": 4, "guidance": 1.0})
        self.assertEqual((kw["num_inference_steps"], kw["guidance_scale"]), (8, 2.0))
        self.assertNotIn("width", kw)

    def test_no_defaults_leaves_the_pipelines_own(self):
        kw = rules.call_kwargs(KLEIN, "a fox", None, 1, None, None)
        self.assertEqual(kw, {"prompt": "a fox"})

    def test_edit_images(self):
        one = rules.call_kwargs(KLEIN, "at night", None, 1, None, None, images=["A"])
        two = rules.call_kwargs(KLEIN, "at night", None, 1, None, None, images=["A", "B"])
        self.assertEqual((one["image"], two["image"]), ("A", ["A", "B"]))
        with self.assertRaises(Refusal):
            rules.call_kwargs(KLEIN, "x", None, 1, None, None, images=["A"] * 11)

    def test_a_model_that_does_not_edit(self):
        with self.assertRaises(Refusal) as c:
            rules.call_kwargs(SDXL, "x", None, 1, None, None, images=["A"])
        self.assertEqual(c.exception.param, "image")

    def test_negative_prompt(self):
        kw = rules.call_kwargs(SDXL, "x", None, 1, None, None, negative="blur")
        self.assertEqual(kw["negative_prompt"], "blur")
        with self.assertRaises(Refusal):
            rules.call_kwargs(KLEIN, "x", None, 1, None, None, negative="blur")

    def test_bare_pipeline_gets_only_what_it_names(self):
        kw = rules.call_kwargs(BARE, "x", (512, 512), 1, 4, 3.0)
        self.assertEqual(kw, {"prompt": "x", "num_inference_steps": 4})
        with self.assertRaises(Refusal) as c:
            rules.call_kwargs(BARE, "x", None, 2, None, None)
        self.assertEqual(c.exception.param, "n")


class Health(unittest.TestCase):
    def test_states(self):
        self.assertEqual(rules.ready_state(True, None), (503, {"status": "loading"}))
        self.assertEqual(rules.ready_state(False, "OSError: no model_index.json")[0], 503)
        self.assertEqual(rules.ready_state(False, None), (200, {"status": "ok"}))


if __name__ == "__main__":
    unittest.main()
