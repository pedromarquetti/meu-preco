package nfce

// Registry of the 27 NFC-e Participant federation units (UFs).
//
// Source of truth: the NFC-e project's official per-UF QR code URL list
// (https://nfce.encat.org/desenvolvedor/qrcode/), whose spreadsheet was
// captured on 2026-09-15. Only the production consultation base is stored; the
// SEFAZ_BASE_URL environment override still swaps it where the deployment
// needs to (homologation, fixtures).
//
// Supported is true only for a UF whose consultation URL was probed with a
// live, publicly available access key and whose response the HTMLParser
// verifiably renders. Verified end-to-end today: São Paulo (35). Every other
// UF is catalogued with the behavior its portal actually exhibited during the
// 2026-09-15 probe run (or a placeholder where no public sample key existed),
// and resolve() answers ErrUnsupportedUF until a fixture-verified Parser is
// added.

// qrParamShape describes how a state embeds the chave/payload in its QR
// consultation URL.
type qrParamShape uint8

const (
	// qrParamP is the national default: {baseURL}?p={chave_acesso}|3|{tpAmb}.
	qrParamP qrParamShape = iota
	// qrParamChNFe is the Amazonas form: {baseURL}?chNFe={chave_acesso}.
	qrParamChNFe
)

// portalKind classifies the visitor-facing behavior of a state's QR page.
type portalKind uint8

const (
	// portalNone is a placeholder until a UF is probed.
	portalNone portalKind = iota
	// portalHTMLFamily groups states whose QR URL returns a server-side resumo
	// DANFE directly (no JavaScript, no captcha hurdle). The HTMLParser covers
	// this family; only SP is currently verified.
	portalHTMLFamily
	// portalServerSide groups states whose QR URL answers with server-rendered
	// HTML but whose document layout is unverified because no public sample
	// receipt was available to probe with (PR, RS).
	portalServerSide
	// portalJSApp groups states whose QR page is a shell or form that needs
	// JavaScript (XHR/SPA) — or a captcha/security-code round-trip — to render
	// the DANFE. Not fetchable with a plain GET.
	portalJSApp
)

// UFConfig is one row of the registry.
type UFConfig struct {
	UF         string // two-digit IBGE code (11–53)
	UFName     string // two-letter abbreviation ("SP")
	FullName   string // human-readable name ("São Paulo")
	QRBaseURL  string // production QR consultation base URL (ENCAT)
	ParamShape qrParamShape
	Portal     portalKind
	Supported  bool
	Notes      string // what the 2026-09-15 probe observed
}

var registry = []UFConfig{
	{UF: "11", UFName: "RO", FullName: "Rondônia", QRBaseURL: "http://www.nfce.sefin.ro.gov.br/consultanfce/consulta.jsp", ParamShape: qrParamP, Portal: portalJSApp, Notes: "national consulta JSP app; JavaScript-dependent."},
	{UF: "12", UFName: "AC", FullName: "Acre", QRBaseURL: "http://www.sefaznet.ac.gov.br/nfce/qrcode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "host unreachable from the probe environment; JSP consulta app."},
	{UF: "13", UFName: "AM", FullName: "Amazonas", QRBaseURL: "http://sistemas.sefaz.am.gov.br/nfceweb/consultarNFCe.jsp", ParamShape: qrParamChNFe, Portal: portalJSApp, Notes: "uses ?chNFe= (not ?p=); JSP consulta app."},
	{UF: "14", UFName: "RR", FullName: "Roraima", QRBaseURL: "https://www.sefaz.rr.gov.br/nfce/servlet/qrcode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "Java servlet QR consulta; no public mod-65 sample key to probe."},
	{UF: "15", UFName: "PA", FullName: "Pará", QRBaseURL: "https://appnfc.sefa.pa.gov.br/portal/view/consultas/nfce/nfceForm.seam", ParamShape: qrParamP, Portal: portalJSApp, Notes: "JSF (Seam) form app; requires JavaScript."},
	{UF: "16", UFName: "AP", FullName: "Amapá", QRBaseURL: "https://www.sefaz.ap.gov.br/nfce/nfcep.php", ParamShape: qrParamP, Portal: portalJSApp, Notes: "PHP consulta; not probed."},
	{UF: "17", UFName: "TO", FullName: "Tocantins", QRBaseURL: "http://www.sefaz.to.gov.br/nfce/qrcode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "not probed."},
	{UF: "21", UFName: "MA", FullName: "Maranhão", QRBaseURL: "http://nfce.sefaz.ma.gov.br/portal/consultarNFCe.jsp", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed with a real public key: 'Consulta Completa de NFCe' JSP app; result via JS/XHR, no server-side DANFE."},
	{UF: "22", UFName: "PI", FullName: "Piauí", QRBaseURL: "http://www.sefaz.pi.gov.br/nfce/qrcode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed: redirects to consulta-nfce-web.prod.apps.ocp4.sefaz.pi.gov.br/nfce/<key> (OpenShift SPA); no server-side DOM."},
	{UF: "23", UFName: "CE", FullName: "Ceará", QRBaseURL: "http://nfce.sefaz.ce.gov.br/pages/ShowNFCe.html", ParamShape: qrParamP, Portal: portalJSApp, Notes: "HTML5 SPA (ShowNFCe.html); renders via JavaScript."},
	{UF: "24", UFName: "RN", FullName: "Rio Grande do Norte", QRBaseURL: "https://nfce.sefaz.rn.gov.br/consultarNFCe.aspx", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed: redirects to portalDFE/NFCe/mDadosNFCe.aspx and gates on a security-code captcha ('Código Gerado')."},
	{UF: "25", UFName: "PB", FullName: "Paraíba", QRBaseURL: "http://www.sefaz.pb.gov.br/nfce", ParamShape: qrParamP, Portal: portalJSApp, Notes: "not probed."},
	{UF: "26", UFName: "PE", FullName: "Pernambuco", QRBaseURL: "http://nfce.sefaz.pe.gov.br/nfce/consulta", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed: host unreachable/timeout from the probe environment."},
	{UF: "27", UFName: "AL", FullName: "Alagoas", QRBaseURL: "http://nfce.sefaz.al.gov.br/QRCode/consultarNFCe.jsp", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed with a real public key: JavaScript-required shell answering 'URL inválida'; no server-side DANFE."},
	{UF: "28", UFName: "SE", FullName: "Sergipe", QRBaseURL: "http://www.nfce.se.gov.br/nfce/qrcode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "not probed."},
	{UF: "29", UFName: "BA", FullName: "Bahia", QRBaseURL: "http://nfe.sefaz.ba.gov.br/servicos/nfce/qrcode.aspx", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed: redirects to NFCEC_consulta_chave_acesso.aspx, a form requiring the printed security code (captcha flow), not a DANFE."},
	{UF: "31", UFName: "MG", FullName: "Minas Gerais", QRBaseURL: "https://portalsped.fazenda.mg.gov.br/portalnfce/sistema/qrcode.xhtml", ParamShape: qrParamP, Portal: portalJSApp, Notes: "portalsped JSF portal; not probed."},
	{UF: "32", UFName: "ES", FullName: "Espírito Santo", QRBaseURL: "http://app.sefaz.es.gov.br/ConsultaNFCe/", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed: frame shell only; no server-side DANFE."},
	{UF: "33", UFName: "RJ", FullName: "Rio de Janeiro", QRBaseURL: "https://consultadfe.fazenda.rj.gov.br/consultaNFCe/QRCode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "probed: returns a JavaScript loader ('Carregando…'); no server-side DOM."},
	{UF: "35", UFName: "SP", FullName: "São Paulo", QRBaseURL: "https://www.nfce.fazenda.sp.gov.br/qrcode", ParamShape: qrParamP, Portal: portalHTMLFamily, Supported: true, Notes: "verified end-to-end with a live public receipt: GET ?p= returns the 'Consulta Resumida' DANFE server-side; an invalid key yields a matching JS-only shell."},
	{UF: "41", UFName: "PR", FullName: "Paraná", QRBaseURL: "http://www.fazenda.pr.gov.br/nfce/qrcode", ParamShape: qrParamP, Portal: portalServerSide, Notes: "probed: GET ?p= answers server-rendered text with no captcha ('chave não consta na base'); real-receipt DOM unverified, no public PR sample key found."},
	{UF: "42", UFName: "SC", FullName: "Santa Catarina", QRBaseURL: "https://sat.sef.sc.gov.br/nfce/consulta", ParamShape: qrParamP, Portal: portalJSApp, Notes: "portalsped-style SAT portal; not probed."},
	{UF: "43", UFName: "RS", FullName: "Rio Grande do Sul", QRBaseURL: "https://www.sefaz.rs.gov.br/NFCE/NFCE-COM.aspx", ParamShape: qrParamP, Portal: portalServerSide, Notes: "probed: redirects to dfe-portal.svrs.rs.gov.br/Dfe/QrCodeNFce and answers server-side codes (it validates the emitter CNPJ check digit inside the chave); real-receipt DOM unverified, no public RS sample key found."},
	{UF: "50", UFName: "MS", FullName: "Mato Grosso do Sul", QRBaseURL: "http://www.dfe.ms.gov.br/nfce/qrcode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "not probed."},
	{UF: "51", UFName: "MT", FullName: "Mato Grosso", QRBaseURL: "http://www.sefaz.mt.gov.br/nfce/consultanfce", ParamShape: qrParamP, Portal: portalJSApp, Notes: "not probed."},
	{UF: "52", UFName: "GO", FullName: "Goiás", QRBaseURL: "https://nfeweb.sefaz.go.gov.br/nfeweb/sites/nfce/danfeNFCe", ParamShape: qrParamP, Portal: portalJSApp, Notes: "nfeweb portal; not probed."},
	{UF: "53", UFName: "DF", FullName: "Distrito Federal", QRBaseURL: "http://www.fazenda.df.gov.br/nfce/qrcode", ParamShape: qrParamP, Portal: portalJSApp, Notes: "not probed."},
}

// LookupUF returns the registry row for a two-digit IBGE code.
func LookupUF(uf string) (UFConfig, bool) {
	for _, cfg := range registry {
		if cfg.UF == uf {
			return cfg, true
		}
	}
	return UFConfig{}, false
}

// SupportedUF reports whether a fixture-verified Parser exists for the state.
func SupportedUF(uf string) bool {
	cfg, ok := LookupUF(uf)
	return ok && cfg.Supported
}

// Registry returns a defensive copy of the full UF table.
func Registry() []UFConfig {
	out := make([]UFConfig, len(registry))
	copy(out, registry)
	return out
}
