package nfce

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

// HTMLParser implements Parser for the São Paulo "Consulta Resumida" HTML.
type HTMLParser struct {
	Logger *slog.Logger
}

var (
	codeRegex    = regexp.MustCompile(`Código:\s*([0-9]+)`)
	numeroRegex  = regexp.MustCompile(`Número:\s*(\d+)`)
	serieRegex   = regexp.MustCompile(`Série:\s*(\d+)`)
	emissaoRegex = regexp.MustCompile(`Emissão:\s*(\d{2}/\d{2}/\d{4})\s+([\d:]{5,8})`)
	protoRegex   = regexp.MustCompile(`Protocolo de Autorização:\s*([0-9]+)`)
	ambRegex     = regexp.MustCompile(`(?i)Ambiente de\s+(Produção|Testes|Homologação)`)
	cnpjFmtRegex = regexp.MustCompile(`CNPJ:\s*([\d./-]+)`)
	cooRegex     = regexp.MustCompile(`COO:\s*(\d+)`)
)

// Parse extracts a normalized domain.Nfce from a raw DANFE HTML page produced
// by the portal. Malformed or missing lines set item-level ParseError values
// rather than failing the entire receipt. A receipt-level Warnings slice holds
// all non-fatal anomalies.
func (p *HTMLParser) Parse(_ context.Context, key AccessKey, raw []byte) (*domain.Nfce, error) {
	if !pageHasDANFE(raw) {
		return nil, fmt.Errorf("%w: body does not contain a DANFE", ErrBadResponse)
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot parse DOM: %v", ErrBadResponse, err)
	}

	n := &domain.Nfce{AccessKey: key.String()}
	n.Supermarket = parseSupermarket(doc)
	n.Items, n.Warnings = parseItems(doc)

	parseTotals(doc, n)
	parsePayment(doc, n)
	parseMetadata(doc, n)
	crossCheckKey(n, key)

	if len(n.Items) == 0 {
		return nil, fmt.Errorf("%w: DANFE contains no items", ErrBadResponse)
	}
	return n, nil
}

// ───────────── emitter ─────────────

func parseSupermarket(doc *goquery.Document) domain.Supermarket {
	em := domain.Supermarket{}
	doc.Find("#u20").Each(func(_ int, s *goquery.Selection) {
		em.LegalName = strings.TrimSpace(s.Text())
	})

	lines := []string{}
	doc.Find("#conteudo .txtCenter div.text").Each(func(_ int, s *goquery.Selection) {
		lines = append(lines, strings.TrimSpace(s.Text()))
	})
	for _, l := range lines {
		if !strings.Contains(l, "CNPJ") {
			continue
		}
		if m := cnpjFmtRegex.FindStringSubmatch(l); len(m) > 1 {
			em.CNPJ = digitsOnly(m[1])
		}
		break
	}
	if len(lines) >= 2 {
		em.Address = strings.TrimSpace(lines[len(lines)-1])
	}
	return em
}

// ───────────── items ─────────────

func parseItems(doc *goquery.Document) ([]domain.NfceItem, []string) {
	var (
		items    []domain.NfceItem
		warnings []string
		line     int
	)

	doc.Find("#tabResult tr").Each(func(_ int, row *goquery.Selection) {
		id, _ := row.Attr("id")
		if !strings.HasPrefix(id, "Item") {
			return
		}
		line++
		item := parseRow(row, line)
		if item.ParseError != "" {
			warnings = append(warnings, fmt.Sprintf("line %d: %s", line, item.ParseError))
		}
		items = append(items, item)
	})
	return items, warnings
}

func parseRow(row *goquery.Selection, line int) domain.NfceItem {
	item := domain.NfceItem{Line: line}
	item.Description = strings.TrimSpace(row.Find("span.txtTit").First().Text())

	if raw := row.Find("span.RCod").First().Text(); raw != "" {
		if m := codeRegex.FindStringSubmatch(raw); len(m) > 1 {
			item.Code = m[1]
		}
	}

	var errs []string
	if q, err := fetchBRDecimal(row, ".Rqtd"); err != nil {
		errs = append(errs, fmt.Sprintf("qty: %v", err))
	} else {
		item.Qty = q
	}

	if u := scalarText(row, ".RUN"); u != "" {
		item.Unit = u
	} else {
		item.Unit = "UN"
	}

	if v, err := fetchBRDecimal(row, ".RvlUnit"); err != nil {
		errs = append(errs, fmt.Sprintf("unit_price: %v", err))
	} else {
		item.UnitPrice = v
	}

	if v, err := fetchBRDecimal(row, ".valor"); err != nil {
		errs = append(errs, fmt.Sprintf("total_price: %v", err))
	} else {
		item.TotalPrice = v
	}

	if len(errs) > 0 {
		item.ParseError = strings.Join(errs, "; ")
	}
	return item
}

// scalarText reads a span's text with its <strong> label element removed, so
// " <strong>Qtde.:</strong>0,815 " yields "0,815".
func scalarText(row *goquery.Selection, css string) string {
	span := row.Find("span" + css).First()
	span.Find("strong").Remove()
	return strings.TrimSpace(span.Text())
}

func fetchBRDecimal(row *goquery.Selection, css string) (decimal.Decimal, error) {
	raw := scalarText(row, css)
	if raw == "" {
		return decimal.Decimal{}, fmt.Errorf("empty value")
	}
	return parseBRDecimal(raw)
}

// ───────────── totals ─────────────

func parseTotals(doc *goquery.Document, n *domain.Nfce) {
	doc.Find("#totalNota #linhaTotal").Each(func(_ int, row *goquery.Selection) {
		label := strings.TrimSpace(row.Find("label").First().Text())
		val := strings.TrimSpace(row.Find("span.totalNumb").First().Text())
		if val == "" {
			return
		}

		switch {
		case strings.Contains(label, "Valor total"):
			n.Total, _ = parseBRDecimal(val)
		case strings.Contains(label, "Descontos"):
			n.Discount, _ = parseBRDecimal(val)
		case strings.Contains(label, "Valor a pagar"):
			n.PaidValue, _ = parseBRDecimal(val)
		}
	})
}

// ───────────── payment ─────────────

func parsePayment(doc *goquery.Document, n *domain.Nfce) {
	var methods []string
	inPayment := false

	doc.Find("#totalNota div").Each(func(_ int, row *goquery.Selection) {
		if id, _ := row.Attr("id"); id == "linhaForma" {
			inPayment = true
			return
		}
		if !inPayment {
			return
		}
		label := strings.TrimSpace(row.Find("label.tx").Text())
		if label == "" || strings.EqualFold(label, "Troco") {
			return
		}
		methods = append(methods, label)
	})
	if len(methods) > 0 {
		n.PaymentMethod = strings.Join(methods, " + ")
	}
}

// ───────────── metadata ─────────────

func parseMetadata(doc *goquery.Document, n *domain.Nfce) {
	doc.Find("#infos .jqm-collapsible").Each(func(_ int, section *goquery.Selection) {
		title := strings.TrimSpace(section.Find("h4").First().Text())
		body := normalizeWS(section.Find("li").First().Text())

		switch {
		case strings.Contains(title, "Informações gerais"):
			if m := numeroRegex.FindStringSubmatch(body); len(m) > 1 {
				n.Number = m[1]
			}
			if m := serieRegex.FindStringSubmatch(body); len(m) > 1 {
				n.Series = m[1]
			}
			if m := emissaoRegex.FindStringSubmatch(body); len(m) > 2 {
				n.IssuedAt = parseEmissao(m[1], m[2])
			}
			if m := protoRegex.FindStringSubmatch(body); len(m) > 1 {
				n.Protocol = m[1]
			}
			if m := ambRegex.FindStringSubmatch(body); len(m) > 1 {
				n.Environment = m[1]
			}

		case strings.Contains(title, "interesse"):
			if m := cooRegex.FindStringSubmatch(body); len(m) > 1 {
				n.COO = m[1]
			}
		}
	})
}

// parseEmissao interprets "dd/mm/yyyy" + "hh:mm:ss" in UTC so the parsed
// value is deterministic across systems (the portal prints BRT wall-clock
// time but no timezone offset).
func parseEmissao(date, clock string) time.Time {
	t, err := time.Parse("02/01/2006 15:04:05", date+" "+clock)
	if err != nil {
		return time.Time{}
	}
	return t
}

// crossCheckKey sets any missing number/series from the access key fields so
// the downstream layer has a consistent picture.
func crossCheckKey(n *domain.Nfce, key AccessKey) {
	if n.Number == "" {
		n.Number = key.Number()
	}
	if n.Series == "" {
		n.Series = key.Series()
	}
}

func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseBRDecimal handles Brazilian money strings: "1.234,56" -> decimal.
// Thousands separator (dot) is stripped; comma separates the fractional part.
func parseBRDecimal(raw string) (decimal.Decimal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "NaN") {
		return decimal.Decimal{}, fmt.Errorf("missing numeric value")
	}
	raw = strings.ReplaceAll(raw, "\u00a0", "") // nbsp

	if idx := strings.LastIndexByte(raw, ','); idx >= 0 {
		intPart := strings.ReplaceAll(raw[:idx], ".", "")
		fracPart := raw[idx+1:]
		raw = intPart + "." + fracPart
	}

	return decimal.NewFromString(raw)
}
