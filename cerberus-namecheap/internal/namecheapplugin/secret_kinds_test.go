package namecheapplugin

import "testing"

// Only api_key is a credential. The user names and the allow-listed address
// are declared kind: name, so the host does not value-redact them out of the
// messages that name them.
func TestOnlyTheAPIKeyIsACredential(t *testing.T) {
	credentials := map[string]bool{}
	for _, req := range Definition().Config.Secrets {
		credentials[req.Name] = req.IsCredential()
	}
	want := map[string]bool{SecretAPIKey: true, SecretAPIUser: false, SecretUsername: false, SecretClientIP: false}
	for name, isCredential := range want {
		got, ok := credentials[name]
		if !ok || got != isCredential {
			t.Errorf("%s: declared=%v credential=%v, want credential=%v", name, ok, got, isCredential)
		}
	}
}
