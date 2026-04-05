package main

import (
	"fmt"
	"net/http"
	"time"
)

// extractHeaders pulls Kronaxis-specific routing metadata from request headers.
func extractHeaders(r *http.Request) RouteRequest {
	tier := 0
	if t := r.Header.Get("X-Kronaxis-Tier"); t != "" {
		fmt.Sscanf(t, "%d", &tier)
	}

	priority := r.Header.Get("X-Kronaxis-Priority")
	if priority == "" {
		priority = "normal"
	}

	return RouteRequest{
		Service:   r.Header.Get("X-Kronaxis-Service"),
		CallType:  r.Header.Get("X-Kronaxis-CallType"),
		Priority:  priority,
		Tier:      tier,
		PersonaID: r.Header.Get("X-Kronaxis-PersonaID"),
	}
}

// loggingMiddleware logs each request with method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		duration := time.Since(start)

		// Don't log health checks to reduce noise
		if r.URL.Path == "/health" {
			return
		}

		logger.Printf("%s %s %d %s [%s %s]",
			r.Method, r.URL.Path, sw.status, duration.Round(time.Millisecond),
			r.Header.Get("X-Kronaxis-Service"),
			r.Header.Get("X-Kronaxis-CallType"),
		)
	})
}

// corsMiddleware adds CORS headers for API access.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Kronaxis-Service, X-Kronaxis-CallType, X-Kronaxis-Priority, X-Kronaxis-Tier, X-Kronaxis-PersonaID")
		w.Header().Set("Access-Control-Expose-Headers", "X-Powered-By, X-Kronaxis-Router-Version, X-Kronaxis-Backend, X-Kronaxis-Rule, X-Kronaxis-Request-Cost")

		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusWriter wraps ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}
