// Package domain holds the entities shared across services, without any
// infrastructure dependencies (no database, no HTTP, no third-party types
// beyond basic numeric helpers).
package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Supermarket is the receipt emitter as printed on the DANFE. It is keyed by
// CNPJ upstream (upsert by CNPJ in persistence). The JSON tags are the stable
// wire names used by the preview receipt snapshot (see internal/service
// snapshot.go); they are not an API contract by themselves.
type Supermarket struct {
	ID        int64     `json:"id"`
	CNPJ      string    `json:"cnpj"` // 14 digits, unformatted
	LegalName string    `json:"legal_name"`
	Address   string    `json:"address"` // free text as printed on the DANFE
	CreatedAt time.Time `json:"created_at"`
}

// Nfce is a normalized, privacy-stripped electronic receipt. Consumer
// identification (name/CPF/CNPJ of the purchaser) is intentionally never
// parsed or stored here. The JSON tags are the stable wire names used by the
// preview receipt snapshot (see internal/service snapshot.go); they are not an
// API contract by themselves.
type Nfce struct {
	ID            int64           `json:"id"`
	AccessKey     string          `json:"access_key"` // 44 digits
	Supermarket   Supermarket     `json:"supermarket"`
	Number        string          `json:"number"`
	Series        string          `json:"series"`
	IssuedAt      *time.Time      `json:"issued_at"`
	Protocol      string          `json:"protocol"`
	Environment   string          `json:"environment"`    // "Produção" | "Homologação"
	PaymentMethod string          `json:"payment_method"` // e.g. "Cartão de Crédito", "Dinheiro", "PIX"
	Total         decimal.Decimal `json:"total"`
	Discount      decimal.Decimal `json:"discount"`
	PaidValue     decimal.Decimal `json:"paid_value"`
	COO           string          `json:"coo"`         // fiscal control number (Controle de Operação de Caixa)
	PayloadRaw    string          `json:"payload_raw"` // stripped DANFE/XML for audit and re-play
	CreatedBy     *int64          `json:"created_by"`
	CreatedAt     time.Time       `json:"created_at"`
	Items         []NfceItem      `json:"items"`
	Warnings      []string        `json:"warnings"` // receipt-level notes about partial/odd parsing
}

// NfceItem is a single receipt line. Money and quantities stay decimal; no
// float is used anywhere in the pipeline. The JSON tags are the stable wire
// names used by the preview receipt snapshot (see internal/service snapshot.go).
type NfceItem struct {
	Line        int             `json:"line"`        // 1-based line number as scanned
	Code        string          `json:"code"`        // cProd as printed (the GTIN candidate when it has 8+ digits)
	Description string          `json:"description"` // xProd
	Qty         decimal.Decimal `json:"qty"`         // Qtde.
	Unit        string          `json:"unit"`        // UN
	UnitPrice   decimal.Decimal `json:"unit_price"`  // Vl. Unit.
	TotalPrice  decimal.Decimal `json:"total_price"` // Vl. Total
	ParseError  string          `json:"parse_error"` // set when the line could not be fully parsed
}
