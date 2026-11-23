package oauth

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
)

// Public are the endpoints that need no sign-in: the discovery documents, the
// token endpoint and registration. Only these answer cross-origin requests
// (browser-based MCP clients call them). Each answers 404 while an
// administrator has MCP switched off.
func (s *Service) Public() map[string]http.Handler {
	m := map[string]http.Handler{
		PathMetadata:    http.HandlerFunc(s.metadata),
		PathResource:    http.HandlerFunc(s.protectedResource),
		PathResourceMCP: http.HandlerFunc(s.protectedResource),
		PathToken:       http.HandlerFunc(s.token),
		PathRegister:    http.HandlerFunc(s.register),
	}
	for p, h := range m {
		m[p] = s.gate(cors(h))
	}
	return m
}

// Gate wraps a handler of the authorization server so that it answers 404 while
// MCP is switched off.
func (s *Service) Gate(next http.Handler) http.Handler { return s.gate(next) }

func (s *Service) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on, err := s.On(r.Context())
		if err != nil {
			s.fail(w, "reading the machine access setting", err)
			return
		}
		if !on {
			// Even the preflight: nothing here exists while it is off.
			oauthError(w, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cors opens a public endpoint to every origin. These endpoints take no cookies
// and no ambient credentials, so this adds no attack surface.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// oauthError is the RFC 6749 section 5.2 error body.
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Service) fail(w http.ResponseWriter, what string, err error) {
	s.log.Error("oauth server error", "during", what, "err", err)
	oauthError(w, http.StatusInternalServerError, "server_error", "the server could not complete the request")
}

func (s *Service) metadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "use GET")
		return
	}
	doc := map[string]any{
		"issuer":                                         s.issuer,
		"authorization_endpoint":                         s.issuer + PathAuthorize,
		"token_endpoint":                                 s.issuer + PathToken,
		"registration_endpoint":                          s.issuer + PathRegister,
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
		"scopes_supported":                               scopesSupported(),
	}
	writeDoc(w, doc)
}

// protectedResource is the RFC 9728 document of /mcp.
func (s *Service) protectedResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "use GET")
		return
	}
	writeDoc(w, map[string]any{
		"resource":                 s.resource,
		"authorization_servers":    []string{s.issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         scopesSupported(),
	})
}

func writeDoc(w http.ResponseWriter, doc map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(doc)
}

// --- the page of a request that cannot be sent back ---------------------------

// pageReason is why an authorization request was refused on a page.
type pageReason int

const (
	reasonNoClientID pageReason = iota
	reasonClient
	reasonRedirect
)

type pageTexts struct {
	lang, title, nothing, noClientID, redirect, clientUnknown, clientDetail, detailClose string
}

var pageEn = pageTexts{lang: "en", title: "Authorization failed", nothing: "Nothing was authorized. You can close this page.",
	noClientID: "The request has no client_id.", redirect: "The redirect_uri is not one the client has registered.",
	clientUnknown: "This client is not registered with this server.", clientDetail: "This client cannot be used: ", detailClose: "."}

var pageZh = pageTexts{lang: "zh-CN", title: "授权失败", nothing: "没有授予任何权限。可以关闭此页面。",
	noClientID: "请求中缺少 client_id。", redirect: "redirect_uri 不是该客户端注册过的地址。",
	clientUnknown: "该客户端没有在此服务器注册。", clientDetail: "无法使用该客户端：", detailClose: "。"}

// WriteProblem answers an authorization request that cannot be redirected: the
// person sees the problem in their language and nothing is sent anywhere.
func (s *Service) WriteProblem(w http.ResponseWriter, r *http.Request, p *Problem) {
	t := pageEn
	if al := strings.ToLower(r.Header.Get("Accept-Language")); strings.HasPrefix(strings.TrimSpace(al), "zh") {
		t = pageZh
	}
	var msg string
	switch p.why {
	case reasonNoClientID:
		msg = t.noClientID
	case reasonRedirect:
		msg = t.redirect
	default:
		switch {
		case p.err == errUnknownClient:
			msg = t.clientUnknown
		case p.err != nil:
			msg = t.clientDetail + p.err.Error() + t.detailClose
		default:
			msg = t.clientDetail
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Accept-Language")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(p.Status)
	fmt.Fprintf(w, `<!doctype html><html lang="%s"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;color:#222}@media(prefers-color-scheme:dark){body{background:#111;color:#ddd}}</style></head>
<body><h1>%s</h1><p>%s</p><p>%s</p></body></html>`,
		html.EscapeString(t.lang), html.EscapeString(t.title), html.EscapeString(t.title), html.EscapeString(msg), html.EscapeString(t.nothing))
}
