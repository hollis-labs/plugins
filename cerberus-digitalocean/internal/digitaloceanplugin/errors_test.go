package digitaloceanplugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func apiError(status int, message string) error {
	return fmt.Errorf("get droplet 42: %w", &godo.ErrorResponse{
		Response: &http.Response{StatusCode: status, Request: &http.Request{Method: "GET", URL: &url.URL{Scheme: "https", Host: "api.digitalocean.com", Path: "/v2/droplets/42"}}},
		Message:  message,
	})
}

// Each failure DigitalOcean can produce is reported with the code the host
// acts on, or uncoded when the host has nothing better to say than the API.
func TestErrorCodes(t *testing.T) {
	unreachable := &url.Error{Op: "Get", URL: "https://api.digitalocean.com/v2/droplets/42",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	cases := []struct {
		name string
		err  error
		want cerbplugin.ErrorCode
	}{
		{"unreachable", unreachable, cerbplugin.ErrorUnavailable},
		{"dns", &net.DNSError{Err: "no such host", Name: "api.digitalocean.com"}, cerbplugin.ErrorUnavailable},
		{"timeout", context.DeadlineExceeded, cerbplugin.ErrorUnavailable},
		{"token rejected", apiError(http.StatusUnauthorized, "Unable to authenticate you"), cerbplugin.ErrorCredentialMissing},
		{"unknown droplet", apiError(http.StatusNotFound, "The resource you were accessing could not be found."), cerbplugin.ErrorInvalidArgs},
		{"forbidden", apiError(http.StatusForbidden, "You are not authorized to perform this operation"), ""},
		{"rate limited", apiError(http.StatusTooManyRequests, "Too many requests"), ""},
		{"server error", apiError(http.StatusInternalServerError, "Server was unable to give you a response."), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := callTool(loadedPlugin(t, &fakeBackend{err: tc.err}), "get_droplet", validArgs("get_droplet"))
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
		"unreachable": &url.Error{Op: "Get", URL: "https://api.digitalocean.com/v2/droplets?token=" + url.QueryEscape(sentinelToken),
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused " + sentinelToken)}},
		"token rejected":  apiError(http.StatusUnauthorized, "rejected token "+sentinelToken),
		"unknown droplet": fmt.Errorf("%w (token %s)", apiError(http.StatusNotFound, "not found"), url.PathEscape(sentinelToken)),
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
			result, err := callTool(p, "get_droplet", validArgs("get_droplet"))
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
