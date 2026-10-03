# OpenAI-compatible adapter fixtures

These are the vendor response bodies the contract tests in `openai_test.go`
replay. They are **not live captures**. Product AI is tested with fakes, and
live vendor calls are opt-in and never use credentials, so no request was
sent to a vendor to make them. Each fixture is adapted from the response
shape in that vendor's public API documentation or open source, then
sanitized:

- IDs, timestamps, fingerprints and token counts are illustrative values in
  the documented format, not values from a real account.
- Message content is replaced with MealQuest dinner text.
- No real credential appears anywhere. `{{KEY}}` is a placeholder the tests
  replace with a fake key, to prove the adapter redacts a key echoed back in
  a vendor body.

The tests check the adapter against these bodies with independent
assertions on both the request sent (path, headers, `response_format`,
messages) and the response decoded. The request side does not depend on the
fixtures.

Below, *not in the public example* marks fields or values that were added
for a test case and do not come from the vendor's documented example.

## OpenAI

Sources: API reference, "The chat completion object" and "List models"
(<https://platform.openai.com/docs/api-reference/chat/object>,
<https://platform.openai.com/docs/api-reference/models/list>); the
Structured Outputs guide, for `refusal`
(<https://platform.openai.com/docs/guides/structured-outputs>); and the error
codes guide (<https://platform.openai.com/docs/guides/error-codes>).

| Fixture | Shape | Not in the public example |
| --- | --- | --- |
| `openai_chat_json_schema.json` | Chat completion object with `usage` details, `service_tier`, `system_fingerprint`. | The JSON reply content. |
| `openai_chat_refusal.json` | Structured Outputs refusal: `content: null`, `refusal` set. | — |
| `openai_models.json` | `{object: list, data: [model]}`. | — |
| `openai_error_invalid_key.json` | Error object, `code: invalid_api_key`, message echoing the key. | The full key in the message; OpenAI masks most of it. Kept whole to test redaction. |
| `openai_error_server.json` | Error object, `type: server_error`. | The `Authorization: Bearer {{KEY}}` text; added to test redaction. |

## OpenRouter

Sources: API reference, chat completion response and errors
(<https://openrouter.ai/docs/api-reference/overview>,
<https://openrouter.ai/docs/api-reference/errors>), and "List available
models" (<https://openrouter.ai/docs/api-reference/list-available-models>).

| Fixture | Shape | Not in the public example |
| --- | --- | --- |
| `openrouter_chat_json_object.json` | Chat completion with `provider`, `native_finish_reason` and `message.reasoning`. | The JSON reply content. |
| `openrouter_chat_upstream_error.json` | An `error` object (`code`, `message`, `metadata.provider_name`, `metadata.raw`) in an HTTP 200 body, which OpenRouter documents for upstream failures. | `user_id`. |
| `openrouter_error_rate_limited.json` | Error object, `code: 429`, rate-limit `metadata`. | The `Key {{KEY}}` text; added to test redaction. |
| `openrouter_models.json` | `{data: [model]}` with `name`, `context_length`, `architecture`, `pricing`, `supported_parameters`. | The second model is a minimal entry, to test optional fields. |

## Ollama

Ollama's OpenAI compatibility page
(<https://github.com/ollama/ollama/blob/main/docs/openai.md>) documents
requests but shows no response bodies. The shapes come from its `openai`
middleware source (<https://github.com/ollama/ollama/blob/main/openai/openai.go>):
`chatcmpl-<n>` IDs, `system_fingerprint: fp_ollama`, `owned_by: library` on
models, and errors as `{error: {message, type: api_error, param, code}}`.

| Fixture | Shape | Not in the public example |
| --- | --- | --- |
| `ollama_chat_text.json` | Plain chat completion. | — |
| `ollama_chat_prompt_only.json` | Chat completion. | The reply text: prose and stray braces around a fenced JSON object, to test prompt-only extraction. |
| `ollama_chat_truncated.json` | Chat completion with `finish_reason: length`. | The cut-off JSON reply. |
| `ollama_models.json` | `{object: list, data: [model]}`. | — |
| `ollama_error_not_found.json` | Middleware error object for a missing model. | — |
