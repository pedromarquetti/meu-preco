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
)

// CaptchaError is returned by a Source when the portal answers with its
// human-verification challenge instead of the receipt. Solving the challenge
// is a later phase; here the error just carries a handle (ChallengeID) and the
// image the user must read, so the API layer can hand them back to the client.
type CaptchaError struct {
	ChallengeID string
	ImageURL    string
}

func (e *CaptchaError) Error() string {
	return fmt.Sprintf("fiscal portal requires a captcha challenge (challenge %s)", e.ChallengeID)
}
