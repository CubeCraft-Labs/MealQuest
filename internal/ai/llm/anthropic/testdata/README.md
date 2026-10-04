# Anthropic adapter fixtures

These are the Messages API response bodies the contract tests in
`anthropic_test.go` replay. They are **not live captures**. Product AI is
tested with fakes, and live vendor calls are opt-in and never use
credentials, so no request was sent to Anthropic to make them. Each fixture
is a sanitized adaptation of the response shapes in Anthropic's public API
documentation:

- Messages: <https://docs.anthropic.com/en/api/messages>
- Tool use, including `tool_choice` `{type: tool}`:
  <https://docs.anthropic.com/en/docs/build-with-claude/tool-use>
- List models: <https://docs.anthropic.com/en/api/models-list>
- Errors: <https://docs.anthropic.com/en/api/errors>

Sanitizing:

- IDs, request IDs, timestamps and token counts are illustrative values in
  the documented format, not values from a real account.
- Message content and tool input are replaced with MealQuest dinner text.
- No real credential appears anywhere. `{{KEY}}` is a placeholder the tests
  replace with a fake key, to prove the adapter redacts a key echoed back in
  a vendor body.

Below, *not in the public example* marks fields or values that were added
for a test case and do not come from the documented example.

| Fixture | Shape | Not in the public example |
| --- | --- | --- |
| `messages_tool_use.json` | Message with one `tool_use` block, `stop_reason: tool_use`, `usage` with cache fields. | The tool name and input. |
| `messages_text.json` | Message with `text` blocks, `stop_reason: end_turn`. | A second text block, to test joining. |
| `messages_max_tokens.json` | Message with a `text` block, `stop_reason: max_tokens`. | — |
| `models_page1.json` | Models list page: `data`, `has_more: true`, `first_id`, `last_id`. | — |
| `models_page2.json` | Last models list page, `has_more: false`. | The second model has no `display_name`, to test the fallback to `id`. |
| `error_authentication.json` | Error, `authentication_error` (401). | The `{{KEY}}` text; added to test redaction. |
| `error_permission.json` | Error, `permission_error` (403). | — |
| `error_rate_limit.json` | Error, `rate_limit_error` (429). | The `{{KEY}}` text; added to test redaction. |
| `error_overloaded.json` | Error, `overloaded_error` (529). | — |
| `error_not_found.json` | Error, `not_found_error` (404). | — |
| `error_invalid_request.json` | Error, `invalid_request_error` (400). | — |
| `error_api.json` | Error, `api_error` (500). | The `x-api-key: {{KEY}}` text; added to test redaction. |
