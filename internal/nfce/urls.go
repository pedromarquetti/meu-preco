package nfce

import "fmt"

// QR code payload layout (Manual DANFE NFC-e QR Code, version 3, online
// emission): the portal consultation URL is
//
//	{baseUrl}?p={chave_acesso}|3|{tpAmb}
//
// where 3 is the QR layout version and tpAmb is 1 (production) or 2
// (homologation). Version 3 dropped the CSC token, so the access key alone is
// enough to build a valid consultation link.
const (
	qrVersion3      = "3"
	envProduction   = 1
	envHomologation = 2
)

// DefaultSPQRBaseURL is the São Paulo NFC-e QR consultation endpoint in
// production (Portal NFC-e SP, "Consulta via QR Code").
const DefaultSPQRBaseURL = "https://www.nfce.fazenda.sp.gov.br/qrcode"

// SPURLBuilder renders the portal URLs used to consult an NFC-e for the São
// Paulo portal. BaseURL can be swapped for an homogation endpoint or a test
// server.
type SPURLBuilder struct {
	BaseURL string
}

// QRCodeURL builds the consultation URL that renders the DANFE for key.
func (b SPURLBuilder) QRCodeURL(key AccessKey, env int) string {
	if b.BaseURL == "" {
		b.BaseURL = DefaultSPQRBaseURL
	}
	return fmt.Sprintf("%s?p=%s|%s|%d", b.BaseURL, key.String(), qrVersion3, env)
}
