package colony

// Builtins is the population a fresh colony ships with: the generalist
// worker, a read-only surveyor, and a mechanical formatter. Each is data,
// not a type (D3), and each carries a concrete verify story matched to what
// it does (doc 06 section 2.4). WorkerCard lives in card.go, next to the
// card contract it is the reference instance of.
func Builtins() []Card {
	return []Card{WorkerCard(), SurveyorCard(), FormatterCard()}
}

// SurveyorCard is the read-only research ant. It reads and fetches, it never
// writes, so its card lists no write or edit tool and the loop physically
// refuses a mutation even if the model asks for one (slice 11). It produces
// Findings, never Patches, so it owes no probe. Its verify story is an
// independent verifier session agreeing its findings are grounded in cited
// files, the check that keeps a survey honest.
func SurveyorCard() Card {
	return Card{
		ID:     "surveyor",
		Name:   "surveyor",
		Glyph:  "σ",
		Accent: "surveyor",
		State: StateSpec{
			Namespace: "surveyor/main",
			Disk:      []string{".ari/sessions"},
		},
		Commands: CommandSpec{
			Accepts:  []string{"prompt"},
			Produces: []string{"finding"},
		},
		Render: RenderSpec{Style: "markdown"},
		Verify: VerifySpec{
			Fixtures: []string{"testdata/surveyor_finding.json"},
			Check:    "an independent verifier session agrees the findings are grounded in the files they cite, with no claim that outruns its evidence",
		},
		Discovery: DiscoverySpec{
			Summary: "A read-only research ant. It answers questions about the current " +
				"repository by reading and searching it and pulling references with " +
				"fetch, and it reports back a Finding cited to the files it read. It " +
				"never changes a file; when it needs a fact it cannot find it asks " +
				"rather than guesses.",
			Classes: []TaskClass{"survey", "research"},
			Signals: []string{"*.go", "*.md", "where", "how", "why", "explain"},
			Prefers: []TaskClass{"survey"},
		},
		Tier:   TierCheap,
		Tools:  []string{"read", "find", "fetch"},
		Status: StatusActive,
	}
}

// FormatterCard is the mechanical ant. It wraps a deterministic tool, gofmt,
// so its work is reproducible and its verify story is the tool exiting zero
// and its output matching a recorded golden. It rewrites files, so it
// produces a Patch and carries the read-only probe D4 requires of a mutator.
func FormatterCard() Card {
	return Card{
		ID:     "formatter",
		Name:   "formatter",
		Glyph:  "μ",
		Accent: "formatter",
		State: StateSpec{
			Namespace: "formatter/main",
			Disk:      []string{".ari/sessions"},
		},
		Commands: CommandSpec{
			Accepts:  []string{"prompt"},
			Produces: []string{"patch"},
		},
		Inspect: InspectSpec{
			Probes: []string{
				"read the file before formatting it",
				"gofmt -l lists what would change before anything is written",
				"git status before and after the change",
			},
		},
		Render: RenderSpec{Style: "markdown"},
		Verify: VerifySpec{
			Fixtures: []string{"testdata/formatter_golden.json"},
			Check:    "gofmt exits zero and the formatted output matches the recorded golden byte for byte",
		},
		Discovery: DiscoverySpec{
			Summary: "A mechanical ant that formats Go source with gofmt. It takes a " +
				"prompt naming files or packages to format, runs the tool, and lands " +
				"the result as a patch. Its work is deterministic, so it is verified " +
				"against a recorded golden rather than a judgment call.",
			Classes: []TaskClass{"mechanical"},
			Signals: []string{"gofmt", "format", "*.go"},
			Prefers: []TaskClass{"mechanical"},
		},
		Tier:   TierLocal,
		Tools:  []string{"read", "edit", "sh"},
		Status: StatusActive,
	}
}
