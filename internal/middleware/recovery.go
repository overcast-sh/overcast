package middleware

import (
	"net/http"
	"runtime/debug"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Recovery catches any panic from a handler, logs it with a stack trace, and
// returns a 500 InternalError response. Without this, a panic in one handler
// would crash the entire server process.
//
// In Go, a "panic" is like an uncaught exception — recover() is the equivalent
// of a catch-all try/catch block.
func Recovery(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					reqID := protocol.RequestIDFromContext(r.Context())
					logger.Error("panic recovered",
						zap.String("request_id", reqID),
						zap.Any("panic", rec),
						zap.ByteString("stack", debug.Stack()),
					)
					// Write a well-formed AWS error so the SDK gets a
					// parseable response rather than an empty connection close.
					writeUnroutedError(w, r, protocol.ErrInternalError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
