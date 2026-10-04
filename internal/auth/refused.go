package auth

import (
	"io"
	"net/http"
)

// refusedPage is shown when a sign-in succeeds at the provider but the
// account is not in the household's group. Signing out ends the provider
// session too, so another account can sign in.
const refusedPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>MealQuest</title>
</head>
<body>
<main>
<h1>This kitchen is for the household only</h1>
<p>The account you signed in with is not part of our kitchen.</p>
<form method="post" action="` + LogoutPath + `">
<button type="submit">Sign out</button>
</form>
</main>
</body>
</html>
`

// refuse writes the refused-sign-in page with 403.
func (a *Auth) refuse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	if _, err := io.WriteString(w, refusedPage); err != nil {
		a.logger.WarnContext(r.Context(), "write refused sign-in page", "error", err)
	}
}
