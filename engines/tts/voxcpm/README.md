# voxcpm

VoxCPM2 (2B, 30 languages, 48 kHz output) behind OpenAI's speech API:
`http://<host>:8000/v1`. Needs a CUDA GPU. Voices are reference samples in a
mounted directory: `<id>.wav`, plus an optional `voices.json` with gender,
langs and `ref_text` per id.

```bash
docker build -t voxcpm .
docker run --gpus all -p 8000:8000 -v "$PWD/voices:/voices" \
  -v hf_cache:/root/.cache/huggingface voxcpm
curl -o hi.wav http://localhost:8000/v1/audio/speech -H 'Content-Type: application/json' \
  -d '{"model":"voxcpm","input":"Привет.","voice":"<id>"}'
```

Chosen on 2026-10-17 after a real phone call as the most natural zero-shot
Russian of those heard: CosyVoice3, OmniVoice, Qwen3-TTS and MOSS-TTS-Realtime
were tried before it.

## HTTP (:8000)

```
POST /v1/audio/speech {"model","input","voice","instructions"?,"response_format"?,"clone","design","cfg_value","timesteps"} → audio/wav (48 kHz PCM16 mono)
POST /tts/stream      {"text","voice_id","style","clone","design","cfg_value","timesteps"} → raw PCM16 LE mono, rate in X-Sample-Rate
GET  /v1/audio/voices → {"voices":[{"id","gender","langs":[],"ref_text"}]}
GET  /health
```

- Voices are the reference samples in `/voices`: id = filename stem, gender/langs from `voices.json`.
- `instructions` is the style.
- `clone`, `design`, `cfg_value`, `timesteps` are **outside OpenAI's API**: trial knobs, extra body fields.
- `/tts/stream` is outside the contract: it exists to measure time to first audio.

## Cloning modes

- **Default: reference** — timbre only from the sample. Chosen by ear on
  2026-10-17 over continuation: it sounded more natural, and it does not
  depend on the sample's transcript.
- **`style`** — reference, plus the style in front of the text in parentheses,
  `(cheerful, a bit faster)text`, the model's own instruction syntax.
- **`clone: "continuation"` / `"combined"`** (trial knob) — the sample and its
  transcript are the prompt and the model continues from them; combined adds
  the reference too. Closer likeness, but only with a clean sample and an
  exact transcript. Our narrator samples end in credits whose names whisper
  mishears; with them, continuation stopped long texts early — counting to
  twenty ended at "четырнадцать, пятнадцать, ши", the same 5.44 s every run,
  whatever voxcpm's `retry_badcase_ratio_threshold`. The short clean sample
  `zifia_kendric` counted to the end in the same mode.

## Voice design — making samples

`{"input", "design": "<description>"}` with no `voice` makes a voice from a
description alone (voxcpm's `(description)text`). Every run is a different
person, so this is not for serving calls: it is how samples are made. A take
worth keeping goes into the voices directory and is cloned from there in reference mode,
which keeps the voice stable from call to call.

Sixteen call personas got their voices this way on 2026-10-17 (`design_<Name>`
in `voices.json`). English descriptions worked for Russian
speech when they said "native Russian speaker". Some takes came with crowd or
room noise in the background — reference cloning carries that into calls;
kept on purpose to hear what it does.

## Things that are not obvious

- **Output is 48 kHz.**
- **`torch.compile` is on** (voxcpm's `optimize=True`, mode reduce-overhead,
  i.e. CUDA graphs), so startup compiles and runs one warmup generation before
  `/health` answers. The image carries g++ and python3-dev for triton.
- **The denoiser is off** (`load_denoiser=False`): a ModelScope model for noisy
  samples; ours are clean.
- **Reference transcripts** come from `openai-whisper` (`REF_ASR_MODEL`,
  default `medium`). A `ref_text` key in
  `voices.json` overrides it. Only the continuation modes need it, so in the
  default mode whisper is never loaded.
- **The prompt is re-encoded on every request**: voxcpm builds its prompt cache
  per call. Fine for a trial; a hold-the-cache path is the first thing to add
  if this engine stays.
- **Generation is serialised** by one lock.

## Measured in a call (2026-10-17, voice Zofija_Kendrick, one RTX 3090)

- **RTF ~0.7–0.95, ~1.1–1.3 on short phrases.** Barely faster than real time, so
  the next batch is often not ready when the current one ends.
- **First audio +2.9–4.0 s** in a phone call
  through the non-streaming door; `/tts/stream` gives its first chunk in
  0.4–1.0 s, but `/v1/audio/speech` answers a whole WAV.
- **A pause after a short first batch.** The first batch of a turn is short by
  design; the second one collects everything the LLM produced meanwhile (305
  bytes once) and takes ~4 s to synthesise while 1.9 s of audio plays.
- **It stopped early on a long list** — counting 1 to 20 ended at "пятнадцать,
  ши". The cause was continuation mode on a sample with a mismatched
  transcript (see Cloning modes), fixed by making reference the default.
- **First request per voice cost ~10 s** (the whisper transcript, gone in
  reference mode), and the first request after a start ~20 s (the prompted
  graph compiling). Startup now warms the default path on the first voice:
  first calls measure 1.6–2.4 s on any voice.
- **The caller's timeout needs ~15 s**: a long
  batch takes 4+ s, and a timeout is silence (no cross-engine fallback).
- **Digits:** reads mixed Latin fine; numbers are safest written out in words by
  whoever produces the text.

## Env

| var | default | |
|---|---|---|
| `MODEL_ID` | `openbmb/VoxCPM2` | HF repo, cached in `/root/.cache/huggingface` |
| `CFG_VALUE` | `2.0` | per-request `cfg_value` wins |
| `TIMESTEPS` | `10` | diffusion steps per patch; per-request `timesteps` wins |
| `REF_ASR_MODEL` | `medium` | whisper model for sample transcripts |
| `VOICES_DIR` | `/voices` | |
