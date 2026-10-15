package forgeplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

// A recorded-shape Forge server and site response carrying fields the DTOs do
// not name: the provider's own id, credentials-adjacent fields, network and
// database details. Decoding into the DTO is the allow-list (ADR 0003): none
// of it may reach CLI output, MCP results or an agent's context.
const fullServerJSON = `{"server":{"id":12,"credential_id":"SENTINEL-CREDENTIAL-ID","name":"web","type":"app","provider":"ocean2",
"provider_id":"SENTINEL-PROVIDER-ID","size":"s-1vcpu-1gb","region":"New York 3","ubuntu_version":"24.04","db_status":null,
"redis_status":null,"php_version":"php83","php_cli_version":"php83","opcache_status":"enabled","database_type":"mysql8",
"ip_address":"192.0.2.5","ssh_port":22,"private_ip_address":"10.99.0.5","local_public_key":"ssh-ed25519 SENTINEL-PUBLIC-KEY",
"blackfire_status":null,"papertrail_status":null,"revoked":false,"created_at":"2026-09-01 12:00:00","is_ready":true,
"tags":[{"name":"SENTINEL-TAG"}],"network":[99]}}`

const fullSiteJSON = `{"sites":[{"id":34,"server_id":12,"name":"example.com","aliases":["SENTINEL-ALIAS"],"directory":"/public",
"wildcards":false,"status":"installed","repository":"acme/site","repository_provider":"github","repository_branch":"main",
"repository_status":"installed","quick_deploy":true,"deployment_status":null,"project_type":"php","app":null,
"php_version":"php83","app_status":null,"slack_channel":"SENTINEL-SLACK","telegram_chat_id":"SENTINEL-TELEGRAM",
"telegram_chat_title":null,"teams_webhook_url":"https://example.invalid/SENTINEL-WEBHOOK","discord_webhook_url":null,
"deployment_url":"https://forge.laravel.com/servers/12/sites/34/deploy/http?token=SENTINEL-DEPLOY-TOKEN",
"deployment_branch":"main","created_at":"2026-09-01 12:00:00","tags":[]}]}`

func TestServerDTOOmitsEverythingNotNamed(t *testing.T) {
	var resp struct {
		Server Server `json:"server"`
	}
	if err := json.Unmarshal([]byte(fullServerJSON), &resp); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(resp.Server)
	got := string(encoded)
	for _, leak := range []string{"SENTINEL", "10.99.0.5", "credential_id", "provider_id", "ssh_port"} {
		if strings.Contains(got, leak) {
			t.Fatalf("serialized server leaked %q:\n%s", leak, got)
		}
	}
	want := `{"id":12,"name":"web","ip_address":"192.0.2.5","region":"New York 3","size":"s-1vcpu-1gb","php_version":"php83","provider":"ocean2","is_ready":true}`
	if got != want {
		t.Fatalf("server =\n %s\nwant (the built-in's field names)\n %s", got, want)
	}
}

// A site's deployment_url carries a deploy-trigger token and its
// notification settings carry webhook URLs; the DTO names neither.
func TestSiteDTOOmitsEverythingNotNamed(t *testing.T) {
	var resp struct {
		Sites []Site `json:"sites"`
	}
	if err := json.Unmarshal([]byte(fullSiteJSON), &resp); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(resp.Sites)
	got := string(encoded)
	for _, leak := range []string{"SENTINEL", "deployment_url", "webhook"} {
		if strings.Contains(got, leak) {
			t.Fatalf("serialized sites leaked %q:\n%s", leak, got)
		}
	}
	want := `[{"id":34,"server_id":12,"name":"example.com","directory":"/public","repository":"acme/site","deployment_branch":"main","status":"installed","deployment_status":""}]`
	if got != want {
		t.Fatalf("sites =\n %s\nwant\n %s", got, want)
	}
}
