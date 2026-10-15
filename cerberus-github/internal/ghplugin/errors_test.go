package ghplugin

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

func githubAPIError(status int, body string) error {
	return fmt.Errorf("get repo o/r: %w", &apiError{StatusCode: status, Body: body})
}

// Each failure GitHub can produce is reported with the code the host acts on,
// or uncoded when the host has nothing better to say than the API.
func TestErrorCodes(t *testing.T) {
	unreachable := &url.Error{Op: "Get", URL: "https://api.github.com/repos/o/r",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	cases := []struct {
		name string
		err  error
		want cerbplugin.ErrorCode
	}{
		{"unreachable", unreachable, cerbplugin.ErrorUnavailable},
		{"dns", &net.DNSError{Err: "no such host", Name: "api.github.com"}, cerbplugin.ErrorUnavailable},
		{"timeout", context.DeadlineExceeded, cerbplugin.ErrorUnavailable},
		{"token rejected", githubAPIError(401, `{"message":"Bad credentials"}`), cerbplugin.ErrorCredentialMissing},
		{"unknown repo", githubAPIError(404, `{"message":"Not Found"}`), cerbplugin.ErrorInvalidArgs},
		{"forbidden", githubAPIError(403, `{"message":"Resource not accessible by personal access token"}`), ""},
		{"rate limited", githubAPIError(429, `{"message":"API rate limit exceeded"}`), ""},
		{"server error", githubAPIError(502, `{"message":"Server Error"}`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := callTool(loadedPlugin(t, &fakeBackend{err: tc.err}), "status", validArgs(""))
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
		"unreachable": &url.Error{Op: "Get", URL: "https://api.github.com/repos/o/r?token=" + url.QueryEscape(sentinelToken),
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused " + sentinelToken)}},
		"token rejected": githubAPIError(401, `{"message":"Bad credentials","echo":"Bearer `+sentinelToken+`"}`),
		"unknown repo":   fmt.Errorf("%w (token %s)", githubAPIError(404, "not found"), url.PathEscape(sentinelToken)),
	}
	for name, backendErr := range cases {
		t.Run(name, func(t *testing.T) {
			p := pluginWithToken(t, &fakeBackend{err: backendErr})
			result, err := callTool(p, "status", validArgs(""))
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
