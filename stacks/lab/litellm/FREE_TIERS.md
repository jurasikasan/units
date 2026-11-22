# Free tiers behind the lab's keys

A snapshot of 2026-11-14 (carried from bot's notes, BOT-744), what the
providers' free tiers allow and what each is good for. Limits change:
before leaning on a number, look again. Each number says where it came
from: **[hdr]** a response header to our request, **[api]** an API answer
to our key, **[measured]** our request timed, **[console]** the account's
page, **[docs]** the provider's documentation, **[forum]** unofficial.

## What for

| work | group in `models.yaml` | why | room |
|---|---|---|---|
| chat with tools | `chat`: Cloudflare glm-4.7-flash → `chat-zai` | both carry a tool-call history and our tool names [measured] | Cloudflare 10k neurons a day; z.ai one request at a time |
| a voice message | `stt`: Groq whisper → `whisper-gpu` | any language, browser formats | Groq 2000 requests, 8 h of sound a day [docs] |
| an utterance in a call | `stt-call`: our GPU → Groq | short, the caller waits | as above |
| speech | our engines (piper, silero, voxcpm) | the free ones speak no Russian, Gemini's aside (10 a day) | — |
| pictures | `images`: Cloudflare FLUX | | flux-1-schnell ~170 a day at 1024² [docs, computed] |

## Providers

### Groq
- Limits [hdr]: gpt-oss-120b, gpt-oss-20b, qwen3.8-27b: 1000 requests a day,
  8000 tokens a minute; [docs]: 30 a minute, 200k tokens a day. Whisper
  large-v3 / turbo: 20 a minute, 2000 a day, 7200 s of sound an hour,
  28800 s a day [docs].
- So: 8k tokens a minute does not fit a long agent prompt at all, and
  200k tokens a day is gone in a day of chat. Right for voice.
- Data: not kept by default; up to 30 days for debugging and abuse,
  switched off in Data Controls [docs].

### z.ai
- Free: glm-4.5-flash, glm-4.7-flash, glm-4.6v-flash (vision) [docs].
- Limits: concurrency only [console]: 4.5-flash 2, 4.7-flash 1, 4.6v-flash 1.
  No daily cap. 4.7-flash often answers 1305 "overloaded" (their capacity,
  not our quota) [measured]: second in `chat`, not first.
- Data: not kept; training not mentioned.

### Cloudflare Workers AI
- 10,000 neurons a day, reset at 00:00 UTC; 300 requests a minute for text
  [docs].
- Cost [docs, measured]: glm-4.7-flash 5,500 / 36,400 neurons per million
  tokens in / out (a short request 0.33 neuron, a long agent prompt ~116);
  whisper-large-v3-turbo 47 a minute of sound (~210 min a day);
  flux-1-schnell 4.8 per 512² tile + 9.6 per step.
- Through its OpenAI door glm-4.7-flash and gemma-4-26b carry a tool-call
  history; gpt-oss-120b fails on it (`content` as an array) [measured].
- Data: "Cloudflare does not use Customer Content … to train AI models"
  [docs].

### Google Gemini (AI Studio)
- Limits of our key [console]: 3.1 / 3.5 Flash Lite 15 a minute, 500 a day;
  the Flash models 5 a minute, 20 a day; TTS 3 a minute, 10 a day; Live API
  unlimited requests.
- Through the OpenAI door a tool-call history is refused without its
  `thought_signature` [measured]; for tests (`lite`), not to run on.
- Data: the free tier may be used to improve products and read by people
  [docs]; for someone in the EEA, Switzerland or the UK the paid terms on
  data apply to the free quota too, but an app offered to other people
  there must be on paid services [docs].

### NVIDIA build (NIM), not in the lab yet
- Limits unpublished, none in headers; ~40 a minute an account [forum], no
  daily cap seen.
- Refuses a tool name with a dot ("Only a-z, A-Z, 0-9, underscores, and
  dashes") [measured]: our MCP tools are `server__tool`, which passes.
- Sometimes holds a request without answering past 90 s [measured]: a short
  timeout and a fallback, if it comes in.
- Data: trial terms, content may be used, not for production [docs].

### Not used
- OpenRouter: 50 requests a day without a one-off $10, 1000 after [docs].
- Cerebras: $5 for 30 days, a card needed [docs]. Mistral: no free models.
  SambaNova: 20 a day.
