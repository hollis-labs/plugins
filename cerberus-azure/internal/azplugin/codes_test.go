package azplugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// Each diagnosis carries the code an agent branches on. A 403 is deliberately
// uncoded: the credential works and is not permitted, which is neither
// unreachable nor missing, and the host reports it as operation_failed.
func TestDescribeErrorCodes(t *testing.T) {
	backend := &sdkBackend{}
	cases := []struct {
		name string
		err  error
		want cerbplugin.ErrorCode
	}{
		{"not signed in", errors.New("AzureCLICredential: ERROR: AADSTS50076: please run 'az login'"), cerbplugin.ErrorCredentialMissing},
		{"rejected credential", &azcore.ResponseError{StatusCode: 401}, cerbplugin.ErrorCredentialMissing},
		{"wrong name", &azcore.ResponseError{StatusCode: 404, ErrorCode: "ResourceNotFound"}, cerbplugin.ErrorInvalidArgs},
		{"unresolvable host", fmt.Errorf("send request: %w", &net.DNSError{Err: "no such host", Name: "management.azure.com"}), cerbplugin.ErrorUnavailable},
		{"refused dial", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, cerbplugin.ErrorUnavailable},
		{"timeout", context.DeadlineExceeded, cerbplugin.ErrorUnavailable},
		{"forbidden", &azcore.ResponseError{StatusCode: 403, ErrorCode: "AuthorizationFailed"}, ""},
		{"anything else", errors.New("boom"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := backend.describeError("list resources", "sub-1", tc.err)
			var coded *cerbplugin.CodedError
			got := cerbplugin.ErrorCode("")
			if errors.As(err, &coded) {
				got = coded.Code
			}
			if got != tc.want {
				t.Fatalf("code = %q, want %q (%v)", got, tc.want, err)
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("the cause fell out of the chain: %v", err)
			}
		})
	}
}
