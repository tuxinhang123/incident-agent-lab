// Command demo-api is the service under diagnosis in the incident-agent lab.
// It serves /health, /ready and /api/data, the latter two depending on Redis.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/tuxinhang/incident-agent-lab/internal/rediskv"
)

const dataKey = "demo:greeting"

type structuredLogger struct {
	enc *json.Encoder
}

func newLogger() *structuredLogger {
	return &structuredLogger{enc: json.NewEncoder(os.Stdout)}
}

func (l *structuredLogger) emit(level, operation string, errType rediskv.ErrorType, msg string, fields map[string]any) {
	entry := map[string]any{
		"ts":        time.Now().UTC().Format(time.RFC3339Nano),
		"level":     level,
		"service":   "demo-api",
		"operation": operation,
		"message":   msg,
	}
	if errType != "" {
		entry["error_type"] = string(errType)
	}
	for k, v := range fields {
		entry[k] = v
	}
	_ = l.enc.Encode(entry)
}

func (l *structuredLogger) error(operation string, e *rediskv.Error) {
	l.emit("error", operation, e.Type, e.Message, nil)
}

func main() {
	addr := envOr("HTTP_ADDR", ":8080")
	redisAddr := envOr("REDIS_ADDR", "127.0.0.1:6379")
	client := rediskv.New(redisAddr, 2*time.Second)
	logger := newLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := client.Ping(); err != nil {
			logger.error("redis_ping", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status":  "unavailable",
				"summary": "redis unavailable",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		value, err := client.Get(dataKey)
		if err != nil {
			if kvErr, ok := err.(*rediskv.Error); ok {
				logger.error("redis_get", kvErr)
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "internal error",
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"key": dataKey, "value": value})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"key": dataKey, "value": value})
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           withLogging(logger, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("demo-api listening on %s (redis %s)", addr, redisAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func withLogging(logger *structuredLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.emit("info", "http_request", "", "request completed", map[string]any{
			"method":      r.Method,
			"path":        r.URL.Path,
			"status_code": rw.status,
			"latency_ms":  time.Since(start).Milliseconds(),
		})
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
