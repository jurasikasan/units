# units

Self-sufficient services and libraries that are useful on their own and can be
assembled into applications: an LLM router, speech in and out, a phone line,
auth, a task board, a file store. Each unit lives in its own directory with its
own `go.mod`, and its own check script.

| unit | what it is |
|---|---|
| [`shared`](shared/) | small Go helpers the units have in common: HTTP, Redis, ops alerts, a fallback loop |
| [`notifier`](notifier/) | operational failures from a Redis stream to a Telegram chat, repeats folded |

More units arrive one at a time, each checked and given a showcase before it
lands here.

## License

Apache-2.0, see [LICENSE](LICENSE).
