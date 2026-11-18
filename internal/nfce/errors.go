// Package nfce implements the fetch + parse layer for electronic receipts.
//
// The package is intentionally source-agnostic: it defines the Source and
// Parser interfaces plus an AccessKey validator, and routes requests to the
// federation state that issued the receipt via the registry in registry.go.
//
// Only the São Paulo NFC-e portal is currently verified end-to-end: it is the
// single federation state whose QR consultation URL returns a complete,
// server-side, JavaScript-free DANFE. Every other state's portal is catalogued
// in the registry with its actual behavior (captcha-gated, JavaScript shell,
// unverified server-rendering) and returns ErrUnsupportedUF until a fixture-
// verified Parser exists for it. Adding a state means implementing its
// Parser/Source pair; the HTTP layer and domain stay untouched.
package nfce

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInvalidKey reports a structurally invalid access key (length,
	// check digit, or model). The API maps it to 422.
	ErrInvalidKey = errors.New("invalid access key")

	// ErrInvalidQRPayload reports a QR code payload that is not a well-formed
	// p= value (see ParseQRPayload). The API maps it to 422.
	ErrInvalidQRPayload = errors.New("invalid QR code payload")

	// ErrUnsupportedUF reports that the receipt belongs to a federation state
	// whose fiscal portal this installation cannot parse yet. The API maps it
	// to 501.
	ErrUnsupportedUF = errors.New("federation state not supported")

	// ErrKeyNotFound reports that the fiscal portal answered but has no
	// document for the requested key. The API maps it to 404.
	ErrKeyNotFound = errors.New("receipt not found on the fiscal portal")

	// ErrUpstream wraps any failure in reaching or rendering from the fiscal
	// portal that is not a captcha challenge or a definitive "not found".
	// The API maps it to 502.
	ErrUpstream = errors.New("fiscal portal error")

	// ErrBadResponse flags portal responses that cannot be a DANFE (empty
	// result, mismatched access key, unexpected layout). The API maps it to 502.
	ErrBadResponse = errors.New("unexpected response from the fiscal portal")

	// ErrAntiBotRejection reports that SEFAZ returned an anti-bot or
	// rate-limit rejection page (e.g., HTTP 200 with "Request Rejected"),
	// rather than a DANFE. The API maps it to 429. AntiBotError carries the
	// safe diagnostic metadata.
	ErrAntiBotRejection = errors.New("SEFAZ portal rejected the request due to anti-bot/rate-limiting")

	// ErrPortalCoolingDown reports that the shared portal limiter is refusing
	// requests until an anti-bot cooldown expires. The API maps it to 429 with
	// a Retry-After header; callers must not sleep through it in a handler.
	ErrPortalCoolingDown = errors.New("fiscal portal is cooling down")

	// ErrPortalBusy reports that a request could not reserve a portal slot
	// before its context deadline. The API maps it to 503 with Retry-After.
	ErrPortalBusy = errors.New("fiscal portal request queue is saturated")

	// ErrWrongSolution reports a captcha answer the portal rejected. The
	// challenge stays usable until its attempt budget is spent.
	ErrWrongSolution = errors.New("captcha solution incorrect")

	// ErrChallengeNotFound reports a challenge_id the store does not hold (a
	// wrong id, one already solved, or one from another process).
	ErrChallengeNotFound = errors.New("captcha challenge not found")

	// ErrChallengeExpired reports a challenge past its TTL.
	ErrChallengeExpired = errors.New("captcha challenge expired")

	// ErrChallengeLocked reports a challenge whose attempt budget is spent.
	ErrChallengeLocked = errors.New("captcha challenge attempt budget exhausted")

	// ErrChallengeReused reports a second solve of an already-solved challenge
	// (replay prevention: a challenge is single-use).
	ErrChallengeReused = errors.New("captcha challenge already used")
)

// AntiBotError is returned when the fiscal portal serves an anti-bot/rate-limit
// rejection page instead of the requested document. It unwraps to
// ErrAntiBotRejection so callers can classify it with errors.Is, while carrying
// only safe diagnostic metadata (never the portal body itself).
type AntiBotError struct {
	// StatusCode is the HTTP status the portal answered with (often 200). It is
	// zero when the block happened at the connection layer, before a response.
	StatusCode int
	// SupportID is the reference ID printed on the rejection page, when any.
	SupportID string
	// BodyBytes is the size of the rejected response body.
	BodyBytes int
	// Reason is a short, safe classification such as "connection dropped by
	// the portal edge". It never contains response bodies or cookie values.
	Reason string
	// RetryAfter is the cooldown the rejection triggered, when known. The API
	// surfaces it as the Retry-After header.
	RetryAfter time.Duration
}

func (e *AntiBotError) Error() string {
	if e.StatusCode == 0 && e.Reason != "" {
		return fmt.Sprintf("fiscal portal rejected the request (%s)", e.Reason)
	}
	if e.SupportID != "" {
		return fmt.Sprintf("fiscal portal rejected the request (status %d, support ID %s)", e.StatusCode, e.SupportID)
	}
	return fmt.Sprintf("fiscal portal rejected the request (status %d)", e.StatusCode)
}

// Unwrap makes errors.Is(err, ErrAntiBotRejection) hold for the typed error.
func (e *AntiBotError) Unwrap() error { return ErrAntiBotRejection }

// PortalCooldownError reports an active anti-bot cooldown. It unwraps to
// ErrPortalCoolingDown and carries a bounded Retry-After for the API.
type PortalCooldownError struct {
	Until      time.Time
	RetryAfter time.Duration
}

func (e *PortalCooldownError) Error() string {
	retry := e.RetryAfter.Round(time.Second)
	if retry < time.Second {
		retry = time.Second
	}
	return fmt.Sprintf("fiscal portal cooling down for %s", retry)
}

// Unwrap makes errors.Is(err, ErrPortalCoolingDown) hold for the typed error.
func (e *PortalCooldownError) Unwrap() error { return ErrPortalCoolingDown }

// PortalBusyError reports that a queued portal request could not start before
// its context deadline. It unwraps to ErrPortalBusy.
type PortalBusyError struct {
	RetryAfter time.Duration
}

func (e *PortalBusyError) Error() string {
	retry := e.RetryAfter.Round(time.Second)
	if retry < time.Second {
		retry = time.Second
	}
	return fmt.Sprintf("fiscal portal request queue is busy for %s", retry)
}

// Unwrap makes errors.Is(err, ErrPortalBusy) hold for the typed error.
func (e *PortalBusyError) Unwrap() error { return ErrPortalBusy }

// CaptchaError is returned by a Source when the portal answers with its
// human-verification challenge instead of the receipt. It carries a handle
// (ChallengeID) and the image the user must read, so the API layer can hand
// them back to the client; the full session context (form action, hidden
// viewstate fields, cookies) lives on the Challenge registered in the
// ChallengeStore, which the solver replays.
type CaptchaError struct {
	ChallengeID string
	ImageURL    string
}

func (e *CaptchaError) Error() string {
	return fmt.Sprintf("fiscal portal requires a captcha challenge (challenge %s)", e.ChallengeID)
}
