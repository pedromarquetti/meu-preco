package nfce

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Access key layout (44 digits, concatenated without separators):
//
//	cUF (2) + AAMM (4) + CNPJ emitente (14) + mod (2) + serie (3) +
//	nNF (9) + tpEmis (1) + cNF (8) + cDV (1)
//
// cDV is the Módulo 11 check digit over the 43 preceding digits.

const (
	modelNFCe = "65"
	keyLen    = 44
)

// AccessKey is a validated 44-digit NFC-e access key. Zero value is invalid.
type AccessKey struct {
	v string
}

// ParseAccessKey normalizes (drops every non-digit, e.g. spaces/masks), then
// validates length, UF code, document model and the check digit.
func ParseAccessKey(raw string) (AccessKey, error) {
	var b strings.Builder
	b.Grow(keyLen)
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	v := b.String()

	if len(v) != keyLen {
		return AccessKey{}, fmt.Errorf("%w: expect exactly 44 digits, got %d", ErrInvalidKey, len(v))
	}
	if uf, err := strconv.Atoi(v[0:2]); err != nil || uf < 11 || uf > 53 {
		return AccessKey{}, fmt.Errorf("%w: unrecognized IBGE UF code %q", ErrInvalidKey, v[0:2])
	}
	if v[20:22] != modelNFCe {
		return AccessKey{}, fmt.Errorf("%w: document model %s, expect 65 (NFC-e)", ErrInvalidKey, v[20:22])
	}
	if !validCheckDigit(v) {
		return AccessKey{}, fmt.Errorf("%w: check digit does not match", ErrInvalidKey)
	}
	return AccessKey{v: v}, nil
}

func (k AccessKey) String() string { return k.v }

// valid reports whether the key is a usable zero-free value.
func (k AccessKey) valid() bool { return len(k.v) == keyLen }

// UFCode returns the IBGE code of the emitting state (positions 1-2).
func (k AccessKey) UFCode() string { return k.v[0:2] }

// EmitterCNPJ returns the emitting company CNPJ (positions 7-20).
func (k AccessKey) EmitterCNPJ() string { return k.v[6:20] }

// Series returns the fiscal series (positions 23-25).
func (k AccessKey) Series() string { return k.v[22:25] }

// Number returns the invoice number (positions 26-34).
func (k AccessKey) Number() string { return k.v[25:34] }

// EmissionMonth decodes AAMM (positions 3-6) into a year and month.
func (k AccessKey) EmissionMonth() (year int, month time.Month, err error) {
	if !k.valid() {
		return 0, 0, fmt.Errorf("access key not parsed")
	}

	ym := k.v[2:6]

	if len(ym) != 4 {
		return 0, 0, fmt.Errorf("access key not parsed")
	}

	yy, err := strconv.Atoi(ym[0:2])
	if err != nil {
		return 0, 0, err
	}
	
	mm, err := strconv.Atoi(ym[2:4])
	if err != nil || mm < 1 || mm > 12 {
		return 0, 0, fmt.Errorf("invalid month in access key")
	}
	year = 2000 + yy

	return year, time.Month(mm), nil
}

// validCheckDigit recomputes the Módulo 11 digit over the 43 leading digits.
// Weights cycle 2..9 from right to left; a remainder < 2 yields DV 0.
func validCheckDigit(v string) bool {
	if len(v) != keyLen {
		return false
	}

	sum := 0
	weight := 2

	for i := keyLen - 2; i >= 0; i-- {
		sum += int(v[i]-'0') * weight
		weight++
		if weight > 9 {
			weight = 2
		}
	}
	rem := sum % 11
	dv := 0
	if rem >= 2 {
		dv = 11 - rem
	}
	return dv == int(v[keyLen-1]-'0')
}
