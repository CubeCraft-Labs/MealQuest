# Ollama adapter fixtures

These are the native Ollama API response bodies the contract tests in
`ollama_test.go` replay. They are **not live captures**. Product AI is tested
with fakes and live vendor calls are opt-in, so no request was sent to an
Ollama server to make them. Each fixture is a sanitized adaptation of the
response shapes in Ollama's public API documentation
(<https://github.com/ollama/ollama/blob/main/docs/api.md>):

- Chat, non-streaming: `POST /api/chat` with `"stream": false`.
- Structured outputs: `POST /api/chat` with a JSON schema in `"format"`.
- List local models: `GET /api/tags`.
- Errors: a JSON `{"error": "..."}` body with a non-2xx status; a model
  that is not pulled is HTTP 404.

Sanitizing:

- Timestamps, durations, sizes, digests and token counts are illustrative
  values in the documented format, not values from a real server.
- Message content is replaced with MealQuest dinner text.
- Ollama takes no credentials, so none appear.

Below, *not in the public example* marks values that were added for a test
case and do not come from the documented example.

| Fixture | Case | Shape | Not in the public example |
| --- | --- | --- | --- |
| `chat_text.json` | Success | Finished chat message, `done_reason: stop`, `prompt_eval_count` and `eval_count`. | — |
| `chat_schema.json` | Schema output | Finished chat message whose content is a JSON object string. | The dinner object. |
| `error_model_not_found.json` | Model missing | `{"error": "model \"…\" not found, try pulling it first"}`, served with HTTP 404. | — |
| `tags.json` | Model list | `{"models": [...]}` with `name`, `model`, `digest` and `details`. | — |

The unreachable-host case has no fixture: there is no response to record.
The tests close an `httptest` server and dial its address, so the
connection is refused, and they hold a reply past a short deadline for the
timeout.
