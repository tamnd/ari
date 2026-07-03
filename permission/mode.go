package permission

// Mode is the session-level permission posture, applied at stage 6,
// after the safety floor and before allow rules, so no mode can approve
// what the floor blocked and no early allow can skip a mode (doc 05
// section 6).
type Mode string

const (
	ModeAsk      Mode = "ask"       // default; the pipeline's default-ask stands
	ModeAutoEdit Mode = "auto-edit" // auto-approve in-tree edits and writes, ask for the rest
	ModeFullAuto Mode = "full-auto" // auto-approve everything the floor allowed
	ModePlan     Mode = "plan"      // read-only; deny every mutating call
)

// permRank orders modes by permissiveness: plan < ask < auto-edit <
// full-auto. Worker narrowing (doc 05 section 10) takes the lower.
func permRank(m Mode) int {
	switch m {
	case ModePlan:
		return 0
	case ModeAutoEdit:
		return 2
	case ModeFullAuto:
		return 3
	default: // ModeAsk and the zero value
		return 1
	}
}

// LeastPermissive returns the more restrictive of two modes.
func LeastPermissive(a, b Mode) Mode {
	if permRank(a) <= permRank(b) {
		return a
	}
	return b
}
