package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ─── Idempotency (plan §36) ───
//
// A cache of completed mutation responses keyed by (scope, idempotency
// key) so a client retry with the same Idempotency-Key gets the original
// answer without re-running the handler (and without a duplicate side
// effect). Stripe / Mailgun / GitHub conventions.
//
// scope is the CALLER identity ("user:123", "api_key:abc", ...), so two
// different callers can use the same Idempotency-Key without colliding —
// the key is only unique within a scope. The composite
// UNIQUE (scope, idempotency_key) enforces that in the database.
//
// Lifecycle of a claimed slot:
//
//	BeginIdempotent  -->  in_progress  -->  FinishIdempotent  -->  done
//	                        |
//	                        +-->  ReleaseIdempotent (handler failed / 5xx)
//
// A retry that lands while the original is in_progress is refused
// 409 IDEMPOTENCY_IN_PROGRESS; a retry after it is done gets the stored
// response replayed; a retry that reuses the key with a DIFFERENT body is
// refused 409 IDEMPOTENCY_KEY_REUSED. The HTTP mapping lives in
// middleware.Idempotency; the store only reports which case occurred via
// the sentinels below.

// Idempotency-state spellings, matching the CHECK constraint on
// idempotency_keys.state.
const (
	idempotencyStateInProgress = "in_progress"
	idempotencyStateDone       = "done"
)

// IdempotencyTTL is how long a completed (or in-flight) slot is kept
// before ExpireIdempotencyKeys may drop it. 24h matches Stripe's standard
// and is generous enough that an out-of-hours retry still replays while
// being small enough that the table does not grow without bound. The
// row's expires_at is stamped created_at+IdempotencyTTL at claim time.
const IdempotencyTTL = 24 * time.Hour

// IdempotencyMaxResponseBody caps the response body cached per slot so one
// misbehaving handler cannot fill the table with megabytes. SDK mutation
// responses are far smaller; anything beyond this is truncated (the live
// response to the client is unaffected).
const IdempotencyMaxResponseBody = 64 * 1024

// Sentinel errors from BeginIdempotent. The middleware maps them onto the
// 409s documented above; nothing else needs to see them.
var (
	// ErrIdempotencyKeyReused: the same (scope, key) was already used
	// with a different request hash. Reusing a key for a semantically
	// different request is a client bug — refuse rather than guess.
	ErrIdempotencyKeyReused = errors.New("idempotency key reused with a different request")

	// ErrIdempotencyInProgress: an earlier request with this (scope, key)
	// is still running and has not yet stored its response. The client
	// should retry with backoff.
	ErrIdempotencyInProgress = errors.New("idempotency key still in progress")
)

// IdempotencyRecord is one idempotency slot: the request fingerprint that
// claimed it and, once done, the response to replay. It is a plain scan
// target (not a bun model) — every read/write here is raw SQL.
type IdempotencyRecord struct {
	ID             int64
	Scope          string
	IdempotencyKey string
	RequestHash    string
	ResponseStatus int
	ResponseBody   []byte
	State          string // idempotencyStateInProgress | idempotencyStateDone
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// BeginIdempotent atomically claims (scope, key) for reqHash, or reports
// what is already there. Exactly one of three things happens:
//
//   - (rec, true, nil): the claim was won. rec.ID identifies the fresh
//     in_progress slot — pass it to FinishIdempotent once the handler has
//     produced its response, or to ReleaseIdempotent if the handler failed.
//
//   - (rec, false, nil): the slot is already done with the SAME request
//     hash. rec.ResponseStatus / rec.ResponseBody are the stored response
//     to replay verbatim; do NOT run the handler.
//
//   - (nil, false, err): the slot is unusable for this request —
//     ErrIdempotencyKeyReused (same key, different request hash) or
//     ErrIdempotencyInProgress (an earlier attempt is still running).
//
// Concurrency: the claim is INSERT ... ON CONFLICT (scope, idempotency_key)
// DO NOTHING RETURNING id. Exactly one concurrent claimant gets the row
// back and is therefore "created"; the losers fall through to the
// read-back and land on one of the two refusals (or a replay). The only
// race left is the row being TTL-cleaned between a losing INSERT and the
// read-back; that is retried as a fresh claim rather than mis-reported.
func (s *Store) BeginIdempotent(ctx context.Context, scope, key, reqHash string) (*IdempotencyRecord, bool, error) {
	// The vanished-row race is vanishingly rare, so two attempts are
	// plenty: one retry covers the cleanup window.
	for attempt := 0; attempt < 2; attempt++ {
		now := time.Now()
		expiresAt := now.Add(IdempotencyTTL)

		var newID int64
		err := s.DB.NewRaw(`
			INSERT INTO idempotency_keys (scope, idempotency_key, request_hash, state, created_at, expires_at)
			VALUES (?, ?, ?, 'in_progress', ?, ?)
			ON CONFLICT (scope, idempotency_key) DO NOTHING
			RETURNING id
		`, scope, key, reqHash, now, expiresAt).Scan(ctx, &newID)
		if err == nil {
			return &IdempotencyRecord{
				ID: newID, Scope: scope, IdempotencyKey: key, RequestHash: reqHash,
				State: idempotencyStateInProgress, CreatedAt: now, ExpiresAt: expiresAt,
			}, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}

		// A row already occupies the slot. Read it back to decide.
		rec := new(IdempotencyRecord)
		err = s.DB.NewRaw(`
			SELECT id, scope, idempotency_key, request_hash,
			       COALESCE(response_status, 0), COALESCE(response_body, ''::bytea),
			       state, created_at, expires_at
			FROM idempotency_keys
			WHERE scope = ? AND idempotency_key = ?
		`, scope, key).Scan(ctx,
			&rec.ID, &rec.Scope, &rec.IdempotencyKey, &rec.RequestHash,
			&rec.ResponseStatus, &rec.ResponseBody, &rec.State, &rec.CreatedAt, &rec.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue // vanished under us — retry the claim
		}
		if err != nil {
			return nil, false, err
		}

		if rec.RequestHash != reqHash {
			return nil, false, ErrIdempotencyKeyReused
		}
		if rec.State == idempotencyStateDone {
			return rec, false, nil
		}
		return nil, false, ErrIdempotencyInProgress
	}
	return nil, false, errors.New("idempotency claim repeatedly raced expiry cleanup; retry")
}

// FinishIdempotent records the final response for a slot claimed by
// BeginIdempotent and flips it to done, so later retries replay it. Only
// the winning claimant calls this. The body is capped at
// IdempotencyMaxResponseBody.
func (s *Store) FinishIdempotent(ctx context.Context, id int64, status int, body []byte) error {
	if len(body) > IdempotencyMaxResponseBody {
		body = body[:IdempotencyMaxResponseBody]
	}
	_, err := s.DB.NewRaw(`
		UPDATE idempotency_keys
		SET response_status = ?, response_body = ?, state = 'done'
		WHERE id = ?
	`, status, body, id).Exec(ctx)
	return err
}

// ReleaseIdempotent drops an in_progress slot the claimant will never
// finish (handler panic, context cancel, or a 5xx we refuse to cache), so
// a retry can claim it fresh instead of being stuck 409
// IDEMPOTENCY_IN_PROGRESS until the TTL lapses. It never touches a done
// row — a completed response is worth keeping.
func (s *Store) ReleaseIdempotent(ctx context.Context, id int64) error {
	_, err := s.DB.NewRaw(`
		DELETE FROM idempotency_keys WHERE id = ? AND state = 'in_progress'
	`, id).Exec(ctx)
	return err
}

// ExpireIdempotencyKeys drops slots whose expires_at is before the given
// instant. The cleanup hook a background timer calls (see
// IdempotencyPruneExpired for the now() convenience). Returns how many
// rows went, purely for observability.
func (s *Store) ExpireIdempotencyKeys(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.DB.NewRaw(`
		DELETE FROM idempotency_keys WHERE expires_at < ?
	`, before).Exec(ctx)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// IdempotencyPruneExpired is ExpireIdempotencyKeys(now()). Kept as the
// name the background janitor already calls.
func (s *Store) IdempotencyPruneExpired(ctx context.Context) (int64, error) {
	return s.ExpireIdempotencyKeys(ctx, time.Now())
}

// ValidIdempotencyKey reports whether a header value is acceptable as an
// idempotency key: non-empty, at most 255 chars (the column's CHECK), and
// free of control characters so a hostile client cannot smuggle newlines
// or NULs into the table.
func ValidIdempotencyKey(s string) bool {
	if len(s) < 1 || len(s) > 255 {
		return false
	}
	return !strings.ContainsAny(s, "\x00\n\r")
}
