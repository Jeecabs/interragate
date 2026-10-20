package model

import (
	"fmt"
	"sort"
)

// ServiceName names a service outside Terraform, as .interragate.yaml
// declares it.
type ServiceName string

// Consumer is a service outside Terraform, such as a site on Vercel or an
// app on phones, that the configuration file says uses what Terraform
// manages. interragate cannot see it; it checks the plan against what the
// file declares.
type Consumer struct {
	Name     ServiceName `json:"name"`
	Platform string      `json:"platform,omitempty"`
	Owners   []string    `json:"owners,omitempty"`
	// Notify is set when the owners may be mentioned, which notifies them.
	Notify bool   `json:"notify,omitempty"`
	Link   string `json:"link,omitempty"`
	// Checked is set when the plan has something the service is declared
	// to use, so the plan could be checked against it.
	Checked bool `json:"checked"`
	// Effects are what the plan does to what it uses, worst first; none
	// when the plan leaves it alone.
	Effects []ConsumerEffect `json:"effects"`
	// Hidden counts what the plan does to it that the configuration's
	// disable or ignore leaves out.
	Hidden int `json:"hidden,omitempty"`
	// Missing lists what it is declared to use that the plan does not
	// have: a typo, or something since renamed or removed.
	Missing []string `json:"missing,omitempty"`
}

// Standing is how a plan leaves a service outside Terraform, for saying so.
type Standing int

// Standings. The zero value is not one.
const (
	StandingUnknown Standing = iota
	// StandingAffected: the plan does something to what it uses.
	StandingAffected
	// StandingHidden: it does, but disable or ignore leaves that out.
	StandingHidden
	// StandingUnaffected: the plan has what it uses and leaves it alone.
	StandingUnaffected
	// StandingUnchecked: the plan has nothing it is declared to use.
	StandingUnchecked
)

// Standing says how the plan leaves the service.
func (c *Consumer) Standing() Standing {
	switch {
	case len(c.Effects) > 0:
		return StandingAffected
	case c.Hidden > 0:
		return StandingHidden
	case c.Checked:
		return StandingUnaffected
	}
	return StandingUnchecked
}

// Worst is the severity of the worst effect on the service, or None.
func (c *Consumer) Worst() Severity {
	worst := None
	for _, e := range c.Effects {
		worst = max(worst, e.Severity)
	}
	return worst
}

// Grouped sorts services outside Terraform by how the plan leaves them,
// for saying so: the affected worst first, each other group in the order
// declared.
func Grouped(cs []*Consumer) map[Standing][]*Consumer {
	out := map[Standing][]*Consumer{}
	for _, c := range ByStanding(cs) {
		out[c.Standing()] = append(out[c.Standing()], c)
	}
	return out
}

// ByStanding sorts services outside Terraform for display: those the plan
// affects, worst first, then the rest in the order declared.
func ByStanding(cs []*Consumer) []*Consumer {
	out := make([]*Consumer, len(cs))
	copy(out, cs)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Standing() == StandingAffected) != (b.Standing() == StandingAffected) {
			return a.Standing() == StandingAffected
		}
		return a.Worst() > b.Worst()
	})
	return out
}

// ConsumerEffect is one thing a plan does to what a service outside
// Terraform uses.
type ConsumerEffect struct {
	Kind     EffectKind `json:"kind"`
	Severity Severity   `json:"severity"`
	// Text says it from the service's side, with code spans in backticks.
	Text string `json:"text"`
	// Update is how a changed value reaches the service, when that matters.
	Update UpdatePath `json:"update,omitempty"`
	// Finding is the ID of the finding that reports it.
	Finding string   `json:"finding,omitempty"`
	Refs    []string `json:"refs,omitempty"`
}

// EffectKind is what a plan does to something a service outside Terraform
// uses.
type EffectKind int

// Effect kinds. The zero value is not one, so an effect left unset cannot
// pass for a kind.
const (
	EffectUnknown EffectKind = iota
	// EffectLoses: what the service uses is destroyed or removed.
	EffectLoses
	// EffectValue: a value the service holds changes, or is known only
	// after apply.
	EffectValue
	// EffectReach: a way in the service uses closes.
	EffectReach
	// EffectOutage: what the service relies on is missing for a while
	// during the apply.
	EffectOutage
)

func (k EffectKind) String() string {
	switch k {
	case EffectUnknown:
		return "unknown"
	case EffectLoses:
		return "loses"
	case EffectValue:
		return "value"
	case EffectReach:
		return "reach"
	case EffectOutage:
		return "outage"
	}
	return fmt.Sprintf("EffectKind(%d)", int(k))
}

// MarshalText writes a kind as its name. An unset kind is an error.
func (k EffectKind) MarshalText() ([]byte, error) {
	if k <= EffectUnknown || k > EffectOutage {
		return nil, fmt.Errorf("model: effect kind %d is not one", int(k))
	}
	return []byte(k.String()), nil
}

// UnmarshalText reads a kind's name.
func (k *EffectKind) UnmarshalText(b []byte) error {
	for _, kind := range []EffectKind{EffectLoses, EffectValue, EffectReach, EffectOutage} {
		if kind.String() == string(b) {
			*k = kind
			return nil
		}
	}
	return fmt.Errorf("model: unknown effect kind %q", b)
}

// UpdatePath is how a changed value reaches a service outside Terraform,
// from the quickest to the slowest.
type UpdatePath int

// Update paths. The zero value means nothing says.
const (
	UpdateUnknown UpdatePath = iota
	// UpdateLive: the service reads the value as it runs, from DNS or a
	// parameter store, and picks up changes by itself.
	UpdateLive
	// UpdateRedeploy: its next deployment takes the value.
	UpdateRedeploy
	// UpdateRebuild: the value is built in, so it needs a new build.
	UpdateRebuild
	// UpdateOTA: an over-the-air update gives the value to installed apps
	// on a compatible build when they next open; the rest keep the old one.
	UpdateOTA
	// UpdateStore: it ships in a store release; copies already installed
	// keep the old value until people update.
	UpdateStore
	// UpdateManual: someone outside the team changes it.
	UpdateManual
)

// UpdatePaths are the update paths a configuration file may name, in order.
func UpdatePaths() []UpdatePath {
	return []UpdatePath{UpdateLive, UpdateRedeploy, UpdateRebuild, UpdateOTA, UpdateStore, UpdateManual}
}

func (u UpdatePath) String() string {
	switch u {
	case UpdateUnknown:
		return ""
	case UpdateLive:
		return "live"
	case UpdateRedeploy:
		return "redeploy"
	case UpdateRebuild:
		return "rebuild"
	case UpdateOTA:
		return "ota"
	case UpdateStore:
		return "store"
	case UpdateManual:
		return "manual"
	}
	return fmt.Sprintf("UpdatePath(%d)", int(u))
}

// ParseUpdatePath reads an update path's name.
func ParseUpdatePath(name string) (UpdatePath, error) {
	for _, u := range UpdatePaths() {
		if u.String() == name {
			return u, nil
		}
	}
	return UpdateUnknown, fmt.Errorf("unknown update %q", name)
}

// MarshalText writes an update path as its name.
func (u UpdatePath) MarshalText() ([]byte, error) {
	if u < UpdateUnknown || u > UpdateManual {
		return nil, fmt.Errorf("model: update path %d is not one", int(u))
	}
	return []byte(u.String()), nil
}

// UnmarshalText reads an update path's name.
func (u *UpdatePath) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*u = UpdateUnknown
		return nil
	}
	v, err := ParseUpdatePath(string(b))
	*u = v
	return err
}
