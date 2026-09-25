package namecheapplugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// Each failure carries the code an agent branches on. An API error Namecheap
// answered for any other reason stays uncoded.
func TestErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want cerbplugin.ErrorCode
	}{
		{"missing credentials", errMissingCredential, cerbplugin.ErrorCredentialMissing},
		{"invalid key", &APIError{Numbers: []string{"1011102"}, Message: "API Key is invalid or API access has not been enabled"}, cerbplugin.ErrorCredentialMissing},
		{"address not allow-listed", &APIError{Numbers: []string{"1011150"}, Message: "Invalid request IP: 198.51.100.7"}, cerbplugin.ErrorCredentialMissing},
		{"address wording only", &APIError{Message: "IP not whitelisted"}, cerbplugin.ErrorCredentialMissing},
		{"unknown domain", &APIError{Numbers: []string{"2019166"}, Message: "Domain not found"}, ""},
		{"refused dial", &url.Error{Op: "Get", URL: APIBaseURL, Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}, cerbplugin.ErrorUnavailable},
		{"no such host", fmt.Errorf("http request: %w", &net.DNSError{Err: "no such host", Name: "api.namecheap.com"}), cerbplugin.ErrorUnavailable},
		{"timeout", context.DeadlineExceeded, cerbplugin.ErrorUnavailable},
		{"server error", errors.New("unexpected status 503: unavailable"), ""},
		{"argument refusal", invalid(errors.New("domain is required")), cerbplugin.ErrorInvalidArgs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorCode(tc.err); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

// A refused address is a configuration problem the operator fixes with
// client_ip, and the message says so.
func TestAddressRefusalNamesClientIP(t *testing.T) {
	msg := MCPAddressRefusal(t)
	for _, want := range []string{"Invalid request IP", "API allow-list", "client_ip", DefaultClientIP, "reload the plugin"} {
		if !strings.Contains(msg, want) {
			t.Errorf("address refusal lost %q: %s", want, msg)
		}
	}
}
