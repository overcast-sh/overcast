// Package middleware provides HTTP middleware for the emulator.
//
// CORS adds Cross-Origin Resource Sharing headers so that browser-based
// AWS SDK clients (e.g. the Overcast web UI) can talk directly to the
// emulator without going through a backend-for-frontend proxy.
//
// This is deliberately permissive — the emulator is a local dev tool,
// not a security boundary. All origins, methods, and headers are allowed.
package middleware

import "net/http"

// CORS returns a middleware that sets permissive CORS headers on every
// response and handles preflight OPTIONS requests.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		// Note: * is invalid for methods/headers per CORS spec; must use explicit lists
		h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
			// Echo requested headers for permissive local-dev behavior and SDK compatibility.
			h.Set("Access-Control-Allow-Headers", reqHeaders)
		} else {
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		h.Set("Access-Control-Expose-Headers", "ETag, X-Amz-*, X-*, Lambda-Web-Request-Id, Lambda-Web-Logstream, Lambda-Web-Error-Type")

		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
