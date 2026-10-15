package k8splugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func codeOf(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

// A VPN-only API server that cannot be reached is unreachable, not a
// credential problem — the distinction an agent branches on. A 403 stays
// uncoded: the identity works and is not permitted.
func TestErrorCodeFollowsTheDiagnosis(t *testing.T) {
	gr := schema.GroupResource{Resource: "pods"}
	dial := &url.Error{Op: "Get", URL: "https://10.0.0.1:6443/api", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	dns := &url.Error{Op: "Get", URL: "https://k8s.example.test/api", Err: &net.DNSError{Err: "no such host", Name: "k8s.example.test"}}
	cases := []struct {
		name string
		err  error
		want cerbplugin.ErrorCode
	}{
		{"refused dial", dial, cerbplugin.ErrorUnavailable},
		{"unresolvable host", dns, cerbplugin.ErrorUnavailable},
		{"timeout", context.DeadlineExceeded, cerbplugin.ErrorUnavailable},
		{"unauthorized", apierrors.NewUnauthorized("expired"), cerbplugin.ErrorCredentialMissing},
		{"exec credential helper", errors.New("Get https://x/api: getting credentials: exec: executable kubelogin not found"), cerbplugin.ErrorCredentialMissing},
		{"not found", apierrors.NewNotFound(gr, "web-0"), cerbplugin.ErrorInvalidArgs},
		{"forbidden", apierrors.NewForbidden(gr, "web-0", errors.New("rbac")), ""},
		{"anything else", errors.New("boom"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := coded(tc.err, fmt.Errorf("list pods: %s", describeError(tc.err, "https://10.0.0.1:6443")))
			if got := codeOf(wrapped); got != tc.want {
				t.Fatalf("code = %q, want %q (%v)", got, tc.want, wrapped)
			}
		})
	}
}

// The code reaches the host as a coded tool result, not an uncoded error.
func TestMarshalResultSendsTheCode(t *testing.T) {
	result, err := marshalResult(List[Pod]{}, cerbplugin.WithCode(cerbplugin.ErrorUnavailable, errors.New("cannot reach the API server")))
	if err != nil {
		t.Fatalf("a coded failure came back as an uncoded error: %v", err)
	}
	code, message, ok := cerbplugin.ParseErrorResult(result.Content)
	if !result.IsError || !ok || code != cerbplugin.ErrorUnavailable || message != "cannot reach the API server" {
		t.Fatalf("result = %+v (%q, %q, %v)", result, code, message, ok)
	}
}
