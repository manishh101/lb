package middleware

import (
	"crypto/subtle"
	"net/http"
)

// BasicAuth creates a middleware that enforces HTTP Basic Authentication.
// If username or password is empty, authentication is skipped (backward compatible).
// Uses constant-time comparison to prevent timing attacks.
// Mirrors Traefik's basicAuth middleware for dashboard protection.
//
// Response codes:
//   - 401 Unauthorized: no credentials provided or missing Authorization header
//   - 403 Forbidden: credentials provided but incorrect
func BasicAuth(username, password string) Middleware {
	return func(next http.Handler) http.Handler {
		// If no credentials configured, skip auth entirely
		if username == "" || password == "" {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			if !ok {
				// No credentials provided — 401 with WWW-Authenticate challenge
				w.Header().Set("WWW-Authenticate", `Basic realm="Load Balancer Dashboard"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			if subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 ||
				subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
				// Credentials provided but wrong — 403 Forbidden
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
