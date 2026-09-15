package nfce

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// maxPayloadLength is a defensive cap on decoded QR payload size. Real v3
// online payloads are < 100 bytes; the offline signature variant can reach a
// few hundred.
const maxPayloadLength = 1 << 16

// pParamRegex finds the p= parameter wherever it sits (first position or after a
// trailing query segment), case-insensitively.
var pParamRegex = regexp.MustCompile(`(?i)[?&]p=`)

// ParseQRPayload normalizes a QR code payload and extracts the access key it
// carries.
//
// The scanner output embeds the payload in a URL: {baseUrl}?p=<payload>. This
// function accepts either that full URL or the raw payload string (URL-encoded
// or not), e.g. any of:
//
//	https://www.nfce.fazenda.sp.gov.br/qrcode?p=3524...0257%7C3%7C1
//	https://www.nfce.fazenda.sp.gov.br/qrcode?p=3524...0257|3|1
//	3524...0257%7C3%7C1
//	3524...0257|3|1
//
// A bare 44-digit key (no "|" segments) is accepted too and normalized to the
// version-3 production payload (key|3|1); a Receita-style payload such as
// key|2|1|csc|hash is accepted and returned verbatim so the exact QR link can
// be replayed against the portal.
//
// The canonical payload returned always starts with the validated 44-digit
// access key, followed by "|"-separated fragments. Returns ErrInvalidQRPayload
// for anything else.
func ParseQRPayload(raw string) (payload string, key AccessKey, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPayloadLength {
		return "", AccessKey{}, errInvalidPayload("empty or oversized payload")
	}

	if i := lastPParam(raw); i >= 0 {
		raw = raw[i:]
	}

	raw = strings.Trim(raw, "&")

	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return "", AccessKey{}, errInvalidPayload("payload is not URL-encoded")
	}

	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		return "", AccessKey{}, errInvalidPayload("empty payload")
	}

	segs := strings.Split(decoded, "|")
	if len(segs) < 1 || len(segs[0]) != keyLen {
		return "", AccessKey{}, errInvalidPayload("payload must start with a 44-digit access key")
	}

	k, err := ParseAccessKey(segs[0])
	if err != nil {
		return "", AccessKey{}, errInvalidPayload(err.Error())
	}

	rest := segs[1:]
	for _, s := range rest {
		// CSC ids are numeric and digests/signatures are hex, so every
		// segment after the key is covered by [0-9a-fA-F].
		if s == "" || !hexSegment(s) {
			return "", AccessKey{}, errInvalidPayload("segments after the key must be non-empty alphanumeric")
		}
	}

	if len(rest) == 0 {
		decoded = fmt.Sprintf("%s|%s|%d", k.String(), qrVersion3, envProduction)
	}

	return decoded, k, nil
}

func errInvalidPayload(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidQRPayload, reason)
}

// lastPParam returns the byte offset of the last [?&]p= occurrence, or -1.
func lastPParam(s string) int {
	matches := pParamRegex.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return -1
	}
	last := matches[len(matches)-1]
	return last[1]
}

func hexSegment(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
