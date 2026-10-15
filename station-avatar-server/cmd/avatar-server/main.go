// Command avatar-server is a process-mode MCP server that generates avatars for
// agents in three modes: svg (a procedural sprite, made by a Hadron workflow),
// image (a model) and hybrid (a model shown the agent's own sprite), falling back
// to svg when the model path cannot run. It is the thin front door: it owns the
// contract, the palette registry and the file store, and delegates drawing.
//
// It is a real, standalone MCP server over stdio with no dependency on Station
// or mcp-host; Station typically spawns and supervises it as a process-mode
// logical server (see ../../station.yaml).
//
// Configuration is environment only:
//
//	AVATAR_WORKFLOW        the svg workflow file as hadrond sees it, an absolute
//	                       path under hadrond's source root (<data dir>/workflows).
//	                       Required: copy workflows/avatar-generate.workflow.yaml there.
//	HADRON_URL             hadrond's API (default http://127.0.0.1:8095)
//	HADRON_TOKEN_FILE      the operator token file (default ~/.hadron/operator.token)
//	AVATAR_TIMEOUT         how long to wait for a workflow run (default 10s)
//	AVATAR_OUT_DIR         where avatars are stored (default ~/.local/share/avatars)
//	AVATAR_REGISTRY        the palette registry file (default <out dir>/palette_registry.json);
//	                       point it at the model tool's file to share slots with it
//	AVATAR_MODEL_DIR       the model tool's checkout (avatar.py, golden/). Unset
//	                       disables image and hybrid: they fall back to svg.
//	AVATAR_PYTHON          its interpreter (default python3)
//	AVATAR_MODEL_KEY_FILE  the model tool's credential file (default the tool's own,
//	                       ~/.config/avatars/avatar-openrouter). Only its presence is checked here.
//	AVATAR_MODEL_LEDGER    the model tool's own spend ledger (default ledger.json in its
//	                       checkout; point it at state outside the checkout)
//	AVATAR_MODEL_SPEND_CAP_USD  the model tool's own hard stop on total spend (default its 9.50)
//	AVATAR_MODEL_EST_USD   expected cost of one model image (default 0.034)
//	AVATAR_MODEL_TIMEOUT   how long to wait for one model call (default 200s)
//	AVATAR_BUDGET_USD      per-call spend ceiling for a paid call (default none)
//	AVATAR_DAILY_CAP_USD   spend limit per UTC day across all paid calls (default none)
//	AVATAR_TOTAL_CAP_USD   spend limit in total, ever (default none)
//	AVATAR_POLICY_FILE     a JSON policy: fallback chains and spend limits (the three
//	                       variables above override it). See internal/policy.
//	AVATAR_SPEND_LEDGER    the plugin's own record of paid calls, which backs the
//	                       daily and total limits (default <out dir>/spend_ledger.json)
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	gmcpserver "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/hadron"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/model"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/policy"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/service"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/spend"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/tools"
)

const version = "0.3.0"

const instructions = "Avatars for agents, as files named after the agent. Call avatar_guide first: it explains the three modes (svg, image, hybrid), " +
	"what each costs, how the fallback to svg works and how palettes are assigned. avatar_generate is idempotent: an agent that already has an avatar gets it back, free."

func main() {
	locator := os.Getenv("AVATAR_WORKFLOW")
	if locator == "" {
		log.Fatal("avatar-server: AVATAR_WORKFLOW is required: the avatar-generate workflow file as hadrond sees it, under its workflow source root")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("avatar-server: no home directory: %v", err)
	}

	client := &hadron.Client{
		BaseURL: env("HADRON_URL", hadron.DefaultBaseURL),
		Token:   hadron.TokenFile(env("HADRON_TOKEN_FILE", filepath.Join(home, ".hadron", "operator.token"))),
		Locator: locator,
		Timeout: duration("AVATAR_TIMEOUT", 10*time.Second),
	}
	outDir := env("AVATAR_OUT_DIR", filepath.Join(home, ".local", "share", "avatars"))

	pol := policy.Default()
	if path := os.Getenv("AVATAR_POLICY_FILE"); path != "" {
		if pol, err = policy.Load(path); err != nil {
			log.Fatalf("avatar-server: %v", err)
		}
	}
	if pol, err = pol.WithEnv(os.Getenv); err != nil {
		log.Fatalf("avatar-server: %v", err)
	}

	var m model.Engine // a true nil unless configured, never a typed nil
	estimate := float64s("AVATAR_MODEL_EST_USD", 0.034)
	if dir := os.Getenv("AVATAR_MODEL_DIR"); dir != "" {
		m = &model.Python{
			Dir: dir, Python: env("AVATAR_PYTHON", "python3"), EstimateUSD: estimate, Timeout: duration("AVATAR_MODEL_TIMEOUT", 200*time.Second),
			KeyFile: os.Getenv("AVATAR_MODEL_KEY_FILE"), LedgerPath: os.Getenv("AVATAR_MODEL_LEDGER"), SpendCapUSD: float64s("AVATAR_MODEL_SPEND_CAP_USD", 0),
		}
	}
	svc := &service.Service{
		Store:    &store.Store{Dir: outDir},
		Registry: &palette.Registry{Path: env("AVATAR_REGISTRY", filepath.Join(outDir, "palette_registry.json"))},
		Engines:  service.Engines(service.HadronSVG{Runner: client}, m, estimate, outDir),
		Policy:   pol,
		Ledger:   &spend.Ledger{Path: env("AVATAR_SPEND_LEDGER", filepath.Join(outDir, "spend_ledger.json"))},
	}

	srv := gmcpserver.NewServer("avatar-server", version, gmcpserver.WithInstructions(instructions))
	tools.Register(srv, tools.Deps{Generator: svc})
	if err := srv.Run(context.Background()); err != nil {
		log.Fatalf("avatar-server: %v", err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func duration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("avatar-server: %s %q: %v", k, v, err)
	}
	return d
}

func float64s(k string, def float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		log.Fatalf("avatar-server: %s %q: want a non-negative number", k, v)
	}
	return f
}
