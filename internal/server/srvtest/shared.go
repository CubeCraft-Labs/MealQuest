package srvtest

// JSON bodies the router writes for the shared session and CSRF refusals.
const (
	UnauthenticatedJSON = `{"error":{"code":"unauthenticated","message":"sign in required","retryable":false}}`
	CSRFJSON            = `{"error":{"code":"csrf","message":"missing or invalid CSRF token","retryable":false}}`
)

// GooglePath is GET /api/v1/integrations/google.
const GooglePath = "/api/v1/integrations/google"
