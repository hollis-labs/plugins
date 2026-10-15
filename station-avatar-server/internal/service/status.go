package service

import (
	"context"
	"os"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/engine"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/policy"
)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ModeStatus is one mode's availability right now.
type ModeStatus struct {
	Mode         engine.Mode         `json:"mode"`
	Available    bool                `json:"available"`
	Reason       engine.Reason       `json:"reason,omitempty"`
	Detail       string              `json:"detail,omitempty"`
	Capabilities engine.Capabilities `json:"capabilities"`
	// Fallback is what this mode falls back to under the policy.
	Fallback []engine.Mode `json:"fallback"`
}

// SpendStatus is what the paid engines have cost against the limits.
type SpendStatus struct {
	Limits      policy.Spend `json:"limits"`
	SpentToday  float64      `json:"spent_today_usd"`
	SpentTotal  float64      `json:"spent_total_usd"`
	Calls       int          `json:"calls"`
	LedgerError string       `json:"ledger_error,omitempty"`
}

// Status is what an operator or agent needs to know before asking for a mode.
type Status struct {
	Modes []ModeStatus `json:"modes"`
	Spend SpendStatus  `json:"spend"`
}

// Status reports each mode's availability and the spend so far. It never makes a
// paid call.
func (s *Service) Status(_ context.Context) Status {
	st := Status{Spend: SpendStatus{Limits: s.Policy.Spend}}
	for _, mode := range []engine.Mode{engine.SVG, engine.Image, engine.Hybrid} {
		ms := ModeStatus{Mode: mode, Fallback: s.Policy.Fallback[mode]}
		if eng := s.Engines[mode]; eng == nil {
			ms.Reason, ms.Detail = engine.Unconfigured, "no engine is configured for this mode"
		} else {
			ms.Capabilities = eng.Capabilities()
			if un := eng.Preflight(); un != nil {
				ms.Reason, ms.Detail = un.Reason, un.Detail
			} else {
				ms.Available = true
			}
		}
		st.Modes = append(st.Modes, ms)
	}
	if s.Ledger != nil {
		t, err := s.Ledger.Totals(s.now())
		if err != nil {
			st.Spend.LedgerError = err.Error()
		} else {
			st.Spend.SpentToday, st.Spend.SpentTotal, st.Spend.Calls = t.Today, t.All, t.Calls
		}
	}
	return st
}
