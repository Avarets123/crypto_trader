package middleware

import (
	"net/http"
	"strings"
)

type contextKey string

const ContextKeyUserID contextKey = "user_id"

// BearerAuth is a placeholder JWT/Bearer middleware.
// Replace token validation logic with your actual implementation.
func BearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// TODO: validate token and inject user_id into context
		next.ServeHTTP(w, r)
	})
}
