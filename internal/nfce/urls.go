package nfce

import (
	"fmt"
	"net/url"
)

// QR code payload layout (Manual DANFE NFC-e QR Code, version 3, online
// emission): the portal consultation URL is
//
//	{baseUrl}?p={chave_acesso}|3|{tpAmb}
//
// where 3 is the QR layout version and tpAmb is 1 (production) or 2
// (homologation). Version 3 dropped the CSC token, so the access key alone is
// enough to build a valid consultation link. The exact concatenated payload
// (v3, or a v2/offline variant carrying a digest and signature) can be
// replayed verbatim too — see ParseQRPayload.
const (
	qrVersion3      = "3"
	envProduction   = 1
	envHomologation = 2
)

// DefaultSPQRBaseURL is the São Paulo NFC-e QR consultation endpoint in
// production (Portal NFC-e SP, "Consulta via QR Code").
var DefaultSPQRBaseURL = func() string {
	cfg, _ := LookupUF("35")
	return cfg.QRBaseURL
}()

// QRBuilder renders the QR consultation URLs for a fiscal portal. BaseURL can
// be swapped for a homologation endpoint or a test server.
type QRBuilder struct {
	BaseURL string
}

func (b QRBuilder) normalized() string {
	if b.BaseURL == "" {
		return DefaultSPQRBaseURL
	}
	return b.BaseURL
}

// FromKey builds the consultation URL that renders the DANFE for key,
// v3 production encoding.
func (b QRBuilder) FromKey(key AccessKey, env int) string {
	return fmt.Sprintf("%s?p=%s|%s|%d", b.normalized(), key.String(), qrVersion3, env)
}

// FromPayload builds the consultation URL replaying an exact QR payload
// (already validated by ParseQRPayload) against the portal.
func (b QRBuilder) FromPayload(payload string) string {
	return b.normalized() + "?p=" + url.QueryEscape(payload)
}
