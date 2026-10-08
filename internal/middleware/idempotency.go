package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// IdempotencyStore is the slice of store the idempotency middleware
// needs. *store.Store satisfies it; the interface lets tests substitute a
// fake so the middleware's claim / replay / refusal logic is covered
// without a database.
type IdempotencyStore interface {
	BeginIdempotent(ctx context.Context, scope, key, requestHash string) (*store.IdempotencyRecord, bool, error)
	FinishIdempotent(ctx context.Context, id int64, status int, body []byte) error
	ReleaseIdempotent(ctx context.Context, id int64) error
}

// Idempotency wraps a mutation handler so a retry carrying the same
// `Idempotency-Key` header returns the original response without
// re-running the handler (Stripe / Mailgun / GitHub conventions).
//
// scope computes the CALLER identity (e.g. "user:123" or "api_key:abc").
// Idempotency keys are only unique within a scope, so two callers using
// the same key never collide.
//
//   - The header is OPTIONAL (opt-in). Without it the request passes
//     straight through — no claim, no cache.
//   - Same key + same request (method+path+body) → the stored response is
//     replayed with an `Idempotent-Replay: true` header.
//   - Same key + DIFFERENT request → 409 IDEMPOTENCY_KEY_REUSED.
//   - Concurrent retry while the first is still running → 409
//     IDEMPOTENCY_IN_PROGRESS (with Retry-After).
//
// The request hash is SHA-256 of method+path+body (plan §36). The body is
// read into memory (capped at 256 KiB) and re-injected so the handler can
// bind it again; oversized requests are refused rather than silently
// bypassing idempotency. Responses are cached for 2xx/4xx (deterministic
// outcomes); 5xx are NOT cached (transient — the slot is released so a
// retry re-executes the handler).
func Idempotency(scope func(*gin.Context) string, s IdempotencyStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		// GET/HEAD are idempotent by HTTP semantics; only mutations need
		// this layer.
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead:
			c.Next()
			return
		}

		key := c.GetHeader("Idempotency-Key")
		if key == "" {
			c.Next() // opt-in: no header, no dedup
			return
		}
		if !store.ValidIdempotencyKey(key) {
			abortWithError(c, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY",
				"Idempotency-Key must be 1–255 chars, no control characters")
			return
		}

		// Read the body so we can fingerprint it and replay it to the
		// handler. Bounded HARD at 256 KiB.
		const maxBody = 256 * 1024
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody+1))
		if err != nil {
			abortWithError(c, http.StatusBadRequest, "BAD_REQUEST",
				"could not read request body")
			return
		}
		if len(body) > maxBody {
			abortWithError(c, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE",
				"request body exceeds idempotency limit (256 KiB)")
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		reqHash := requestHash(c.Request.Method, c.Request.URL.Path, body)
		rec, created, err := s.BeginIdempotent(c.Request.Context(), scope(c), key, reqHash)
		if err != nil {
			if errors.Is(err, store.ErrIdempotencyKeyReused) {
				abortWithError(c, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED",
					"Idempotency-Key was already used for a different request")
				return
			}
			if errors.Is(err, store.ErrIdempotencyInProgress) {
				c.Header("Retry-After", "1")
				abortWithError(c, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS",
					"an earlier request with this Idempotency-Key is still being processed")
				return
			}
			abortInternal(c, err)
			return
		}

		if !created {
			// Cache hit: replay the stored response verbatim.
			c.Header("Idempotent-Replay", "true")
			c.Data(rec.ResponseStatus, "application/json; charset=utf-8", rec.ResponseBody)
			c.Abort()
			return
		}

		// We own the slot (rec.ID). Wrap the writer to capture the
		// response, run the handler, then store 2xx/4xx / release 5xx.
		writer := &captureWriter{ResponseWriter: c.Writer}
		c.Writer = writer

		defer func() {
			detached := context.WithoutCancel(c.Request.Context())
			status := writer.status
			if status == 0 || status >= 500 {
				// Crashed before a status, or transient 5xx: don't cache,
				// release so a retry can claim the slot fresh.
				_ = s.ReleaseIdempotent(detached, rec.ID)
				return
			}
			_ = s.FinishIdempotent(detached, rec.ID, status, writer.buf.Bytes())
		}()

		c.Next()
	}
}

// requestHash fingerprints method+path+body as a 64-char SHA-256 hex
// string — the value stored in idempotency_keys.request_hash.
func requestHash(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// captureWriter mirrors writes to the real response while keeping a
// copy for the idempotency cache. status defaults to 200 if the handler
// uses c.JSON / c.String without explicit WriteHeader.
type captureWriter struct {
	gin.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (w *captureWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.buf.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}
