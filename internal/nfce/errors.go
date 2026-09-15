// Package nfce implements the fetch + parse layer for electronic receipts.
//
// The package is intentionally source-agnostic: it defines the Source and
// Parser interfaces plus an AccessKey validator, and ships one concrete
// provider for the São Paulo NFC-e portal (São Paulo is the coordinating UF of
// the NFC-e project). Adding another federation state later means adding one
// more Source/Parser implementation, not touching the HTTP layer or domain.
package nfce

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidKey reports a structurally invalid access key (length,
	// check digit, or model). The API maps it to 422.
	ErrInvalidKey = errors.New("invalid access key")

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
