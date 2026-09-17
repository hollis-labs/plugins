package cfplugin

import (
	cf "github.com/leefowlercu/go-contextforge/contextforge"
)

// The types in this file are the security boundary required by
// docs/adr/0003-connector-response-dtos.md in the Cerberus repo.
//
// go-contextforge's Gateway carries AuthToken, AuthPassword, AuthHeaderValue,
// AuthValue, AuthUsername, AuthHeaders, AuthQueryParamValue and OAuthConfig —
// live upstream credentials for every MCP server behind the gateway. A
// ContextForge gateway is the only place that auth can be set, so returning the
// SDK struct would emit those into CLI stdout, daemon logs, MCP tool results
// and an agent's context window at once.
//
// So these DTOs are an allow-list. A field the vendor adds in a minor release
// is not emitted unless someone adds it here on purpose. For credentials we
// expose the *shape* — auth_type, and whether a secret is configured — never a
// value. That is the `probe-*` convention: names, never values, so output is
// safe to paste into a document.

// Gateway is the Cerberus view of an upstream MCP server registration.
type Gateway struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Transport   string `json:"transport,omitempty"`
	Enabled     bool   `json:"enabled"`
	Reachable   bool   `json:"reachable"`

	// AuthType is the kind of auth configured ("bearer", "basic", …), never a
	// value. AuthConfigured reports whether any credential is set, so an
	// operator can tell "no auth" from "auth I cannot see".
	AuthType       string `json:"auth_type,omitempty"`
	AuthConfigured bool   `json:"auth_configured"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// VirtualServer is the Cerberus view of a composed catalog.
type VirtualServer struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Description         string   `json:"description,omitempty"`
	Enabled             bool     `json:"enabled"`
	IsActive            bool     `json:"is_active"`
	AssociatedTools     []string `json:"associated_tools,omitempty"`
	AssociatedResources []string `json:"associated_resources,omitempty"`
	AssociatedPrompts   []string `json:"associated_prompts,omitempty"`

	// OAuthEnabled is a flag, never the OAuthConfig blob the SDK carries.
	OAuthEnabled bool `json:"oauth_enabled"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Tool is the Cerberus view of a gateway-prefixed tool registration.
type Tool struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Visibility  string `json:"visibility,omitempty"`
}

// Health is the Cerberus view of the open /health endpoint.
type Health struct {
	OK      bool   `json:"ok"`
	Status  string `json:"status,omitempty"`
	Address string `json:"address"`
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func formatTimestamp(t *cf.Timestamp) string {
	if t == nil {
		return ""
	}
	return t.Time.UTC().Format("2006-01-02T15:04:05Z")
}

// gatewayAuthConfigured reports whether the gateway carries any credential.
// It deliberately never returns, logs or formats the values it inspects.
func gatewayAuthConfigured(g *cf.Gateway) bool {
	if g == nil {
		return false
	}
	for _, v := range []*string{
		g.AuthToken,
		g.AuthPassword,
		g.AuthUsername,
		g.AuthHeaderValue,
		g.AuthValue,
		g.AuthQueryParamValue,
	} {
		if v != nil && *v != "" {
			return true
		}
	}
	return len(g.AuthHeaders) > 0 || len(g.OAuthConfig) > 0
}

// GatewayFromSDK maps one vendor gateway onto our allow-list. This function is
// the security boundary; read it as one.
func GatewayFromSDK(g *cf.Gateway) Gateway {
	if g == nil {
		return Gateway{}
	}
	return Gateway{
		ID:             derefString(g.ID),
		Name:           g.Name,
		URL:            g.URL,
		Description:    derefString(g.Description),
		Transport:      g.Transport,
		Enabled:        g.Enabled,
		Reachable:      g.Reachable,
		AuthType:       derefString(g.AuthType),
		AuthConfigured: gatewayAuthConfigured(g),
		CreatedAt:      formatTimestamp(g.CreatedAt),
		UpdatedAt:      formatTimestamp(g.UpdatedAt),
		LastSeen:       formatTimestamp(g.LastSeen),
	}
}

func GatewaysFromSDK(in []*cf.Gateway) []Gateway {
	out := make([]Gateway, 0, len(in))
	for _, g := range in {
		out = append(out, GatewayFromSDK(g))
	}
	return out
}

func VirtualServerFromSDK(s *cf.Server) VirtualServer {
	if s == nil {
		return VirtualServer{}
	}
	return VirtualServer{
		ID:                  s.ID,
		Name:                s.Name,
		Description:         derefString(s.Description),
		Enabled:             s.Enabled,
		IsActive:            s.IsActive,
		AssociatedTools:     s.AssociatedTools,
		AssociatedResources: s.AssociatedResources,
		AssociatedPrompts:   s.AssociatedPrompts,
		OAuthEnabled:        s.OAuthEnabled,
		CreatedAt:           formatTimestamp(s.CreatedAt),
		UpdatedAt:           formatTimestamp(s.UpdatedAt),
	}
}

func VirtualServersFromSDK(in []*cf.Server) []VirtualServer {
	out := make([]VirtualServer, 0, len(in))
	for _, s := range in {
		out = append(out, VirtualServerFromSDK(s))
	}
	return out
}

func ToolFromSDK(t *cf.Tool) Tool {
	if t == nil {
		return Tool{}
	}
	return Tool{
		ID:          t.ID,
		Name:        t.Name,
		Description: derefString(t.Description),
		Enabled:     t.Enabled,
		Visibility:  t.Visibility,
	}
}

func ToolsFromSDK(in []*cf.Tool) []Tool {
	out := make([]Tool, 0, len(in))
	for _, t := range in {
		out = append(out, ToolFromSDK(t))
	}
	return out
}
