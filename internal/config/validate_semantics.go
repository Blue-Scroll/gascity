package config

import (
	"fmt"
	"strings"
)

const idleSleepMaskedByIdleTimeoutWarningFragment = "idle_timeout and sleep_after_idle are both set; idle_timeout takes precedence"

// IsIdleSleepMaskedByIdleTimeoutWarning reports whether warning describes the
// supported idle-timeout precedence case where sleep_after_idle is masked.
func IsIdleSleepMaskedByIdleTimeoutWarning(warning string) bool {
	return strings.Contains(warning, idleSleepMaskedByIdleTimeoutWarningFragment)
}

// ValidateSemantics checks cross-entity semantic constraints in the config
// and returns warnings for issues that cannot be caught by individual struct
// validation. Unlike ValidateAgents (which returns hard errors), semantic
// warnings are non-fatal — they indicate likely misconfigurations but don't
// prevent the system from starting.
func ValidateSemantics(cfg *City, source string) []string {
	var warnings []string

	// Build known provider name set from the explicit catalog.
	knownProviders := make(map[string]bool)
	for name := range cfg.Providers {
		knownProviders[name] = true
	}

	// Check provider references on agents.
	for _, a := range cfg.Agents {
		if a.Provider == "" || a.StartCommand != "" {
			continue // no provider lookup needed
		}
		if !knownProviders[a.Provider] {
			warnings = append(warnings, fmt.Sprintf(
				"%s: agent %q: provider %q is not defined in [providers]",
				source, a.QualifiedName(), a.Provider))
		}
	}

	// Check workspace default provider.
	if p := cfg.Workspace.Provider; p != "" {
		if !knownProviders[p] {
			warnings = append(warnings, fmt.Sprintf(
				"%s: [workspace] provider %q is not defined in [providers]",
				source, p))
		}
	}

	// Check agent default provider.
	if p := cfg.AgentDefaults.Provider; p != "" {
		if !knownProviders[p] {
			warnings = append(warnings, fmt.Sprintf(
				"%s: [agent_defaults] provider %q is not a built-in or city-defined provider",
				source, p))
		}
	}

	// Check agent session field.
	for _, a := range cfg.Agents {
		if !IsValidSessionTransport(a.Session) {
			warnings = append(warnings, fmt.Sprintf(
				"%s: agent %q: session %q is not a valid session transport (use \"acp\", \"tmux\", or omit)",
				source, a.QualifiedName(), a.Session))
		}
	}

	// Check namepool on unlimited pools (discovery uses prefix matching,
	// which won't find themed names).
	for _, a := range cfg.Agents {
		if a.Namepool != "" && a.MaxActiveSessions != nil && *a.MaxActiveSessions < 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%s: agent %q: namepool requires bounded max_active_sessions (> 0); unlimited agents use prefix discovery which cannot find themed names",
				source, a.QualifiedName()))
		}
	}

	// A pool name must belong to exactly one slot of exactly one agent.
	warnings = append(warnings, namepoolIdentityCollisions(cfg, source)...)

	// Check overlapping idle lifecycle controls.
	for _, a := range cfg.Agents {
		if a.IdleTimeout != "" && a.SleepAfterIdle != "" {
			warnings = append(warnings, fmt.Sprintf(
				"%s: agent %q: %s and sleep_after_idle only applies when the session survives the idle_timeout check",
				source, a.QualifiedName(), idleSleepMaskedByIdleTimeoutWarningFragment))
		}
	}

	// Custom provider names must not contain the reserved ":" character
	// (used by the base = "builtin:..." / "provider:..." namespace prefixes).
	for name := range cfg.Providers {
		if strings.Contains(name, ":") {
			warnings = append(warnings, fmt.Sprintf(
				"%s: [providers.%s] custom provider name contains reserved character \":\" (used for \"builtin:\" / \"provider:\" namespace prefixes on base field)",
				source, name))
		}
	}

	// Validate base field grammar when set.
	for name, spec := range cfg.Providers {
		if spec.Base == nil {
			continue
		}
		bv := *spec.Base
		if bv == "" {
			continue // explicit standalone opt-out is valid
		}
		switch {
		case strings.HasPrefix(bv, BasePrefixBuiltin):
			suffix := strings.TrimPrefix(bv, BasePrefixBuiltin)
			if suffix == "" {
				warnings = append(warnings, fmt.Sprintf(
					"%s: [providers.%s] base %q has empty suffix after %q prefix",
					source, name, bv, BasePrefixBuiltin))
			}
		case strings.HasPrefix(bv, BasePrefixProvider):
			suffix := strings.TrimPrefix(bv, BasePrefixProvider)
			if suffix == "" {
				warnings = append(warnings, fmt.Sprintf(
					"%s: [providers.%s] base %q has empty suffix after %q prefix",
					source, name, bv, BasePrefixProvider))
			}
		}
	}

	// Validate options_schema_merge grammar.
	for name, spec := range cfg.Providers {
		switch spec.OptionsSchemaMerge {
		case "", "replace", "by_key":
			// valid
		default:
			warnings = append(warnings, fmt.Sprintf(
				"%s: [providers.%s] options_schema_merge must be \"replace\" or \"by_key\", got %q",
				source, name, spec.OptionsSchemaMerge))
		}
	}

	// Check PromptMode on city-defined providers.
	for name, spec := range cfg.Providers {
		switch spec.PromptMode {
		case "", "arg", "flag", "none":
			// valid
		default:
			warnings = append(warnings, fmt.Sprintf(
				"%s: [providers.%s] prompt_mode must be \"arg\", \"flag\", \"none\", or empty, got %q",
				source, name, spec.PromptMode))
		}
		if spec.PromptMode == "flag" && spec.PromptFlag == "" {
			warnings = append(warnings, fmt.Sprintf(
				"%s: [providers.%s] prompt_flag is required when prompt_mode = \"flag\"",
				source, name))
		}
	}

	return warnings
}

// namepoolIdentityCollisions reports every pool name that more than one slot
// can be handed. A pool session's identity is its name qualified by the agent's
// dir and binding, so a namepool name IS the session identity: "vessel-network/
// nux" names one session and says nothing about which pool it came from.
//
// When two agents in one dir offer the same name, that one identity means two
// sessions. They claim the same beads, read the same mail, and each one's
// work_dir stamp overwrites the other's, which is how two sessions ended up in
// one git worktree and deleted each other's uncommitted work (vn-rm9u8g,
// twice in fifteen minutes on 2026-08-23). The same is true of one agent
// listing a name twice: slot 2 and slot 5 then resolve to one identity.
//
// It is a warning, not a hard error, on purpose. A town whose pools already
// overlap must still start, and the operator sees this in "gc doctor" (the
// config-semantics check) instead of a dead city.
func namepoolIdentityCollisions(cfg *City, source string) []string {
	if cfg == nil {
		return nil
	}
	var warnings []string
	// identity -> the qualified agent name that claimed it first.
	claimed := make(map[string]string)
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		owner := a.QualifiedName()
		for _, name := range a.NamepoolNames {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			identity := a.QualifiedInstanceName(name)
			first, taken := claimed[identity]
			if !taken {
				claimed[identity] = owner
				continue
			}
			if first == owner {
				warnings = append(warnings, fmt.Sprintf(
					"%s: agent %q lists pool name %q more than once; two slots would share session identity %q, so two sessions would share one work_dir",
					source, owner, name, identity))
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"%s: agents %q and %q both offer pool name %q, so both resolve to session identity %q; a pool name belongs to exactly one agent, or one identity means two sessions in one work_dir",
				source, first, owner, name, identity))
		}
	}
	return warnings
}
