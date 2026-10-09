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

// CaptchaError is returned by a Source when the portal answers with its
// human-verification challenge instead of the receipt. It carries a handle
// (ChallengeID) and the image the user must read, so the API layer can hand
// them back to the client; the full session context (form action, hidden
// viewstate fields, cookies) lives on the Challenge registered in the
// ChallengeStore, which the solver replays in Phase 6.
type CaptchaError struct {
	ChallengeID string
	ImageURL    string
}

func (e *CaptchaError) Error() string {
	return fmt.Sprintf("fiscal portal requires a captcha challenge (challenge %s)", e.ChallengeID)
}
