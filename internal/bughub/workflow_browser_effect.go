package bughub

const (
	BrowserEffectConfirmed = "confirmed"
	BrowserEffectNoEffect  = "no_effect"
	BrowserEffectBlocked   = "blocked"
	BrowserEffectAmbiguous = "ambiguous"
	BrowserEffectUncertain = "uncertain"
)

// BrowserStepEffectEvaluation is the host-owned result of comparing one
// decision's declared effects with its action receipt and fresh after-scene.
type BrowserStepEffectEvaluation struct {
	Outcome          string   `json:"outcome"`
	ConfirmedKinds   []string `json:"confirmed_kinds"`
	UnconfirmedKinds []string `json:"unconfirmed_kinds"`
	BlockCode        string   `json:"block_code,omitempty"`
}
