package caddyplugin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// TLS modes. Both keep every listener on the supplied bind address. There is
// deliberately no mode that obtains certificates over HTTP-01 or TLS-ALPN-01,
// because those need public inbound reachability and this plugin never makes a
// host publicly reachable.
const (
	TLSCloudflareDNS = "cloudflare-dns"
	TLSInternal      = "internal"
)

// DefaultConfigPath is where render reads the current file and apply writes.
const DefaultConfigPath = "/etc/caddy/Caddyfile"

// DefaultTokenEnv is the environment variable name the rendered file reads the
// DNS-01 token from, through Caddy's {env.NAME} placeholder.
const DefaultTokenEnv = "CLOUDFLARE_API_TOKEN"

// loopbackUpstream is the only upstream address a route can have.
const loopbackUpstream = "127.0.0.1"

var (
	labelRe    = `[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?`
	nameRe     = regexp.MustCompile(`^` + labelRe + `$`)
	hostRe     = regexp.MustCompile(`^` + labelRe + `(?:\.` + labelRe + `)*$`)
	domainRe   = regexp.MustCompile(`^` + labelRe + `(?:\.` + labelRe + `)+$`)
	emailRe    = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	tokenEnvRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Route is one declared route: <name>.<host>.<domain> proxied to a loopback
// port. Host is optional; without it the name is <name>.<domain>.
type Route struct {
	Name   string `json:"name"`
	Host   string `json:"host,omitempty"`
	Domain string `json:"domain"`
	Port   int    `json:"port"`
}

// RenderInput is the validated declaration a Caddyfile is rendered from.
type RenderInput struct {
	Routes     []Route
	Bind       netip.Addr
	TLS        string
	Email      string
	TokenEnv   string
	ConfigPath string
}

func invalid(format string, a ...any) error {
	return cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, fmt.Errorf(format, a...))
}

// parseRenderInput reads and validates the arguments shared by render and
// apply. Every value that ends up inside the Caddyfile passes an allow-list
// pattern here, so a route cannot inject Caddyfile syntax.
func parseRenderInput(raw map[string]any) (RenderInput, error) {
	var in RenderInput
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	bind := argString(raw, "bind")
	if bind == "" {
		fail("bind is required: the tailnet IP Caddy listens on")
	} else if addr, err := parseBind(bind); err != nil {
		fail("%v", err)
	} else {
		in.Bind = addr
	}

	in.TLS = argString(raw, "tls")
	if in.TLS == "" {
		in.TLS = TLSCloudflareDNS
	}
	if in.TLS != TLSCloudflareDNS && in.TLS != TLSInternal {
		fail("tls %q is not one of %s, %s", in.TLS, TLSCloudflareDNS, TLSInternal)
	}

	in.Email = argString(raw, "email")
	if in.Email != "" && !emailRe.MatchString(in.Email) {
		fail("email is not a plain address")
	}

	in.TokenEnv = argString(raw, "token_env")
	if in.TokenEnv == "" {
		in.TokenEnv = DefaultTokenEnv
	}
	if !tokenEnvRe.MatchString(in.TokenEnv) {
		fail("token_env must be an environment variable name such as %s", DefaultTokenEnv)
	}

	in.ConfigPath = argString(raw, "config_path")
	if in.ConfigPath == "" {
		in.ConfigPath = DefaultConfigPath
	}

	routes, err := parseRoutes(raw["routes"])
	if err != nil {
		fail("%v", err)
	}
	if len(routes) == 0 && err == nil {
		fail("routes is required: at least one route")
	}
	seen := map[string]bool{}
	for i, r := range routes {
		label := fmt.Sprintf("routes[%d]", i)
		r.Name, r.Host, r.Domain = strings.ToLower(r.Name), strings.ToLower(r.Host), strings.ToLower(r.Domain)
		if !nameRe.MatchString(r.Name) {
			fail("%s name %q must be one DNS label (letters, digits, hyphen)", label, r.Name)
		}
		if r.Host != "" && !hostRe.MatchString(r.Host) {
			fail("%s host %q must be DNS labels (letters, digits, hyphen)", label, r.Host)
		}
		if !domainRe.MatchString(r.Domain) {
			fail("%s domain %q must be a domain such as example.com", label, r.Domain)
		}
		if r.Port < 1 || r.Port > 65535 {
			fail("%s port %d must be between 1 and 65535", label, r.Port)
		}
		routes[i] = r
		fqdn := r.fqdn()
		if seen[fqdn] {
			fail("%s duplicates %s", label, fqdn)
		}
		seen[fqdn] = true
	}
	in.Routes = routes

	if len(problems) > 0 {
		return RenderInput{}, invalid("invalid arguments: %s", strings.Join(problems, "; "))
	}
	return in, nil
}

// parseBind accepts only an address that is not publicly routable: a tailnet
// address or loopback. A wildcard or public address is refused outright, so
// there is no input that makes Caddy listen on a public interface.
func parseBind(text string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("bind %q is not an IP address", text)
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsMulticast() {
		return netip.Addr{}, fmt.Errorf("bind %q would listen on every interface; supply the tailnet IP", text)
	}
	if addr.IsLoopback() || tailnetV4.Contains(addr) || tailnetV6.Contains(addr) {
		return addr, nil
	}
	return netip.Addr{}, fmt.Errorf("bind %q is not a tailnet (100.64.0.0/10, fd7a:115c:a1e0::/48) or loopback address; public binds are not supported", text)
}

func (r Route) fqdn() string {
	if r.Host == "" {
		return r.Name + "." + r.Domain
	}
	return r.Name + "." + r.Host + "." + r.Domain
}

// wildcard is the site address covering the route: one label below host.domain.
func (r Route) wildcard() string {
	if r.Host == "" {
		return "*." + r.Domain
	}
	return "*." + r.Host + "." + r.Domain
}

func argString(raw map[string]any, key string) string {
	switch v := raw[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// parseRoutes accepts the array a JSON caller sends, or the JSON text a CLI
// `--arg routes=[...]` delivers.
func parseRoutes(value any) ([]Route, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		var routes []Route
		dec := json.NewDecoder(strings.NewReader(v))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&routes); err != nil {
			return nil, fmt.Errorf("routes is not a JSON array of {name, host, domain, port}: %v", err)
		}
		return routes, nil
	case []any:
		routes := make([]Route, 0, len(v))
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("routes[%d] must be an object", i)
			}
			for key := range m {
				switch key {
				case "name", "host", "domain", "port":
				default:
					return nil, fmt.Errorf("routes[%d] has unknown field %q", i, key)
				}
			}
			port, err := portOf(m["port"])
			if err != nil {
				return nil, fmt.Errorf("routes[%d] %v", i, err)
			}
			routes = append(routes, Route{
				Name:   argString(m, "name"),
				Host:   argString(m, "host"),
				Domain: argString(m, "domain"),
				Port:   port,
			})
		}
		return routes, nil
	case []Route:
		return v, nil
	default:
		return nil, fmt.Errorf("routes must be an array of {name, host, domain, port}")
	}
}

func portOf(value any) (int, error) {
	switch v := value.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, fmt.Errorf("port must be a whole number")
		}
		return int(v), nil
	case int:
		return v, nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("port %q is not a number", v)
		}
		return n, nil
	case nil:
		return 0, fmt.Errorf("port is required")
	default:
		return 0, fmt.Errorf("port must be a number")
	}
}

// tokenPlaceholder is the Caddy placeholder for the DNS-01 token.
func (in RenderInput) tokenPlaceholder() string {
	if in.TLS != TLSCloudflareDNS {
		return ""
	}
	return "{env." + in.TokenEnv + "}"
}

// renderCaddyfile produces the Caddyfile text. Output is deterministic: sites
// and the routes inside them are sorted, so a no-op apply is a byte-for-byte
// match and a diff shows only real changes.
func renderCaddyfile(in RenderInput) string {
	groups := map[string][]Route{}
	for _, r := range in.Routes {
		groups[r.wildcard()] = append(groups[r.wildcard()], r)
	}
	sites := make([]string, 0, len(groups))
	for site := range groups {
		sites = append(sites, site)
	}
	sort.Strings(sites)

	var b strings.Builder
	b.WriteString("# Managed by Cerberus (caddy plugin). Edits are overwritten by `apply`.\n")
	b.WriteString("{\n")
	b.WriteString("\tdefault_bind " + in.Bind.String() + "\n")
	if in.Email != "" {
		b.WriteString("\temail " + in.Email + "\n")
	}
	b.WriteString("}\n")

	for _, site := range sites {
		routes := groups[site]
		sort.Slice(routes, func(i, j int) bool { return routes[i].Name < routes[j].Name })

		b.WriteString("\n" + site + " {\n")
		switch in.TLS {
		case TLSCloudflareDNS:
			b.WriteString("\ttls {\n")
			b.WriteString("\t\tdns cloudflare " + in.tokenPlaceholder() + "\n")
			b.WriteString("\t}\n")
		case TLSInternal:
			b.WriteString("\ttls internal\n")
		}
		for _, r := range routes {
			b.WriteString("\n\t@" + r.Name + " host " + r.fqdn() + "\n")
			b.WriteString("\thandle @" + r.Name + " {\n")
			b.WriteString("\t\treverse_proxy " + loopbackUpstream + ":" + strconv.Itoa(r.Port) + "\n")
			b.WriteString("\t}\n")
		}
		b.WriteString("\n\thandle {\n\t\tabort\n\t}\n")
		b.WriteString("}\n")
	}
	return b.String()
}
