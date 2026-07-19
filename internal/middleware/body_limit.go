package middleware

import "net/http"

// RequestBodyLimit rejects declared oversized requests and caps streaming or
// chunked request bodies. Handlers that read the wrapped body can recognize
// *http.MaxBytesError and return StatusRequestEntityTooLarge.
func RequestBodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
