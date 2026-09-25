package forgeplugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func forgeAPIError(status int, body string) error {
	return fmt.Errorf("forge get server: %w", &apiError{StatusCode: status, Body: body})
}

// Each failure Forge can produce is reported with the code the host acts on,
// or uncoded when the host has nothing better to say than the API.
func TestErrorCodes(t *testing.T) {
	unreachable := &url.Error{Op: "Get", URL: "https://forge.laravel.com/api/v1/servers/12",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	cases := []struct {
		name string
		err  error
		want cerbplugin.ErrorCode
	}{
		{"unreachable", unreachable, cerbplugin.ErrorUnavailable},
		{"dns", &net.DNSError{Err: "no such host", Name: "forge.laravel.com"}, cerbplugin.ErrorUnavailable},
		{"timeout", context.DeadlineExceeded, cerbplugin.ErrorUnavailable},
		{"token rejected", forgeAPIError(401, `{"message":"Unauthenticated."}`), cerbplugin.ErrorCredentialMissing},
		{"unknown server", forgeAPIError(404, `{"message":"Not Found."}`), cerbplugin.ErrorInvalidArgs},
		{"forbidden", forgeAPIError(403, `{"message":"This action is unauthorized."}`), ""},
		{"rate limited", forgeAPIError(429, `{"message":"Too Many Attempts."}`), ""},
		{"server error", forgeAPIError(500, `{"message":"Server Error"}`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := callTool(loadedPlugin(t, &fakeBackend{err: tc.err}), "get_server", validArgs("get_server"))
			if tc.want == "" {
				if err == nil {
					t.Fatalf("result = %s, want it left uncoded", result.Content)
				}
				return
			}
			code, message := failure(t, result, err)
			if code != tc.want {
				t.Fatalf("code = %q (%s), want %q", code, message, tc.want)
			}
		})
	}
}

// The code is read before the scrub drops the error chain, and the coded
// message is scrubbed like any other: a coded result carries no token.
func TestCodedErrorsNeverCarryTheToken(t *testing.T) {
	cases := map[string]error{
		"unreachable": &url.Error{Op: "Get", URL: "https://forge.laravel.com/api/v1/servers?token=" + url.QueryEscape(sentinelToken),
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused " + sentinelToken)}},
		"token rejected": forgeAPIError(401, `{"message":"Unauthenticated.","echo":"Bearer `+sentinelToken+`"}`),
		"unknown server": fmt.Errorf("%w (token %s)", forgeAPIError(404, "not found"), url.PathEscape(sentinelToken)),
	}
	for name, backendErr := range cases {
		t.Run(name, func(t *testing.T) {
			p := &Plugin{newBackend: func(string) Backend { return &fakeBackend{err: backendErr} }}
			if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretAPIToken: sentinelToken}}); err != nil {
				t.Fatalf("Init: %v", err)
			}
			if _, err := p.Load(context.Background()); err != nil {
				t.Fatalf("Load: %v", err)
			}
			result, err := callTool(p, "get_server", validArgs("get_server"))
			if err != nil {
				t.Fatalf("came back uncoded: %v", err)
			}
			code, message := failure(t, result, err)
			if code == "" {
				t.Fatalf("result = %s, want a code", result.Content)
			}
			for _, form := range []string{sentinelToken, url.QueryEscape(sentinelToken), url.PathEscape(sentinelToken)} {
				if strings.Contains(message, form) || strings.Contains(string(result.Content), form) {
					t.Fatalf("the coded result leaked the token (%q):\n%s", form, result.Content)
				}
			}
		})
	}
}
