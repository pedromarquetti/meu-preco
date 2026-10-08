// Package domain holds the entities shared across services, without any
// infrastructure dependencies (no database, no HTTP, no third-party types
// beyond basic numeric helpers).
package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Supermarket is the receipt emitter as printed on the DANFE. It is keyed by
// CNPJ upstream (upsert by CNPJ in persistence).
type Supermarket struct {
	ID        int64
	CNPJ      string // 14 digits, unformatted
	LegalName string
	Address   string // free text as printed on the DANFE
	CreatedAt time.Time
}

// Nfce is a normalized, privacy-stripped electronic receipt. Consumer
// identification (name/CPF/CNPJ of the purchaser) is intentionally never
// parsed or stored here.
type Nfce struct {
	ID            int64
	AccessKey     string // 44 digits
	Supermarket   Supermarket
	Number        string
	Series        string
	IssuedAt      *time.Time
	Protocol      string
	Environment   string // "Produção" | "Homologação"
	PaymentMethod string // e.g. "Cartão de Crédito", "Dinheiro", "PIX"
	Total         decimal.Decimal
	Discount      decimal.Decimal
	PaidValue     decimal.Decimal
	COO           string // fiscal control number (Controle de Operação de Caixa)
	PayloadRaw    string // stripped DANFE/XML for audit and re-play
	CreatedBy     *int64
	CreatedAt     time.Time
	Items         []NfceItem
	Warnings      []string // receipt-level notes about partial/odd parsing
}

// NfceItem is a single receipt line. Money and quantities stay decimal; no
// float is used anywhere in the pipeline.
type NfceItem struct {
	Line        int             // 1-based line number as scanned
	Code        string          // cProd as printed (the GTIN candidate when it has 8+ digits)
	Description string          // xProd
	Qty         decimal.Decimal // Qtde.
	Unit        string          // UN
	UnitPrice   decimal.Decimal // Vl. Unit.
	TotalPrice  decimal.Decimal // Vl. Total
	ParseError  string          // set when the line could not be fully parsed
}
