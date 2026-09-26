// cmd/compat/shadow.go — step 2 of the native-group migration.
//
// docs/plans/compat-coverage-modelgen.md §3.11 migrates a hand-written group to
// an authored IR scenario in three steps: author it under the same names, run
// both implementations in parallel through one nightly soak cycle with every
// (suite, test) result matching its native predecessor exactly, then delete the
// per-language implementations in the PR that flips the group over.
//
// Step 2 is what this file measures. While the port soaks, the authored copy
// runs beside the native one under a shadow name — `<group>-shadow` — and its
// registry entry carries `shadowOf: <group>`. `--compare-shadow` reads a run
// report, joins the two on (suite, test), and reports every pair that answered
// differently.
//
// It reports rather than gates, in the sense that nothing about a divergence is
// automatically anybody's fault: §3.11 says a divergence blocks the deletion,
// never the gate, and is triaged as either an IR expressiveness gap or a latent
// bug in one of the eight copies. But it exits non-zero, because the one thing
// it must never do is let a flip PR cite a comparison nobody read.
package main

import (
	"fmt"
	"io"
	"sort"

	"github.com/overcast-sh/overcast/compat"
)

// shadowPair is one (suite, test) as both halves answered it.
type shadowPair struct {
	Suite string
	Test  string
	// Native and Shadow are the statuses reported for the native group and for
	// its shadow. An empty string is "the suite reported nothing here", which
	// is a verdict of its own: a comparison that quietly skipped a pair would
	// report agreement it never observed.
	Native compat.Status
	Shadow compat.Status
	// NativeReason and ShadowReason are the skip reasons, which decide which
	// kind of skip a skip is — see verdict.
	NativeReason string
	ShadowReason string
}

// A pair's verdict. Three of the four outcomes are not "the port is wrong",
// and collapsing them into one would make the comparison unusable for the
// migration it exists to serve.
type shadowVerdict int

const (
	// shadowAgree: both halves answered, identically. What the soak is for.
	shadowAgree shadowVerdict = iota
	// shadowDiverge: they answered differently, and the difference is not one
	// of the two below. This is the only outcome that blocks the flip.
	shadowDiverge
	// shadowDebtClosed: the suite never implemented the native group and says
	// so with the shared not-implemented sentinel, while the shadow ran and did
	// not fail. There is no predecessor to match, and burning that debt down is
	// the point of the migration (§3.11 step 4: "a ported group implemented by
	// scenario counts as implemented in every suite with a backend"). Reported,
	// never a blocker.
	//
	// A shadow that FAILS against that skip is not debt closed: it is a port
	// that does not work in exactly the suites it was meant to cover, with no
	// native there to diverge from, so it is classified as a divergence and
	// blocks the flip. Counting it as debt closed is how a node-js-sdk
	// interpreter bug went unseen (#2246). A shadow answering "unimplemented"
	// still closes the debt — the natives elsewhere report the same 501, and
	// that is the emulator's gap, not the port's.
	shadowDebtClosed
	// shadowUnexercised: neither half ran — a setup that failed the same way
	// twice, a suite the run did not reach, a group scoped away. The statuses
	// are equal and the pair proves nothing, which is exactly promote.go's
	// reasoning about an all-skip candidate. Reported apart from agreement so
	// a flip PR cannot cite it as evidence, but not a blocker: an environment
	// that could not run either half says nothing about the port either way.
	shadowUnexercised
)

func (p shadowPair) verdict() shadowVerdict {
	nativeRan := p.Native != "" && p.Native != compat.StatusSkip
	shadowRan := p.Shadow != "" && p.Shadow != compat.StatusSkip
	switch {
	case !nativeRan && !shadowRan:
		return shadowUnexercised
	case p.Native == compat.StatusSkip && isNotImplementedSkip(p.NativeReason) && shadowRan && p.Shadow != compat.StatusFail:
		return shadowDebtClosed
	case p.Native == p.Shadow:
		return shadowAgree
	default:
		return shadowDiverge
	}
}

// shadowComparison is one --compare-shadow run's verdict for one shadow group.
type shadowComparison struct {
	Shadow string
	Native string
	// Pairs is every (suite, test) considered, sorted by suite then test.
	Pairs []shadowPair
}

func (c shadowComparison) of(want shadowVerdict) []shadowPair {
	var out []shadowPair
	for _, p := range c.Pairs {
		if p.verdict() == want {
			out = append(out, p)
		}
	}
	return out
}

func (c shadowComparison) divergences() []shadowPair { return c.of(shadowDiverge) }

// groupStatuses indexes one report's results for one group by suite/test.
//
// It is deliberately a copy of promote.go's runStatuses rather than a shared
// helper: that one keys by "suite/test" for a ledger comparison across runs,
// this one keys by the pair so the two halves can be joined and the suite named
// on its own in the report. Merging them would mean one function with a flag.
func groupStatuses(report *compat.RunReport, group string) map[shadowKey]shadowAnswer {
	out := make(map[shadowKey]shadowAnswer)
	if report == nil {
		return out
	}
	for _, suite := range report.Suites {
		if suite == nil {
			continue
		}
		for _, g := range suite.Groups {
			if g == nil || g.Name != group {
				continue
			}
			for _, test := range g.Tests {
				out[shadowKey{Suite: suite.Suite, Test: test.Test}] = shadowAnswer{
					Status: test.Status,
					Reason: test.Error,
				}
			}
		}
	}
	return out
}

type shadowKey struct{ Suite, Test string }

// shadowAnswer is one half's answer: the status, and the message beside it —
// which for a skip is the reason, and is what tells parity debt apart from a
// setup that fell over.
type shadowAnswer struct {
	Status compat.Status
	Reason string
}

// compareShadows joins every shadow group in the generated registry against the
// hand-written group it names, from one run report.
func compareShadows(gen *generatedRegistry, report *compat.RunReport) []shadowComparison {
	var out []shadowComparison
	for _, g := range gen.Groups {
		if g.ShadowOf == "" {
			continue
		}
		shadow := groupStatuses(report, g.Name)
		native := groupStatuses(report, g.ShadowOf)
		keys := make(map[shadowKey]bool, len(shadow)+len(native))
		for k := range shadow {
			keys[k] = true
		}
		for k := range native {
			// The native group's tests are the shadow's, by
			// lintGeneratedRegistry, so the two key sets differ only where a
			// suite reported one half and not the other — which is exactly
			// what a comparison must not lose.
			keys[k] = true
		}
		comparison := shadowComparison{Shadow: g.Name, Native: g.ShadowOf}
		for k := range keys {
			comparison.Pairs = append(comparison.Pairs, shadowPair{
				Suite:        k.Suite,
				Test:         k.Test,
				Native:       native[k].Status,
				Shadow:       shadow[k].Status,
				NativeReason: native[k].Reason,
				ShadowReason: shadow[k].Reason,
			})
		}
		sort.Slice(comparison.Pairs, func(i, j int) bool {
			if comparison.Pairs[i].Suite != comparison.Pairs[j].Suite {
				return comparison.Pairs[i].Suite < comparison.Pairs[j].Suite
			}
			return comparison.Pairs[i].Test < comparison.Pairs[j].Test
		})
		out = append(out, comparison)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Shadow < out[j].Shadow })
	return out
}

// writeShadowReport renders the comparisons and reports whether the port is
// clear to land. Only a divergence blocks it; the other two non-agreements are
// printed under their own labels so a flip PR quoting the summary cannot pass
// off "nothing ran" as "everything matched".
func writeShadowReport(w io.Writer, comparisons []shadowComparison, annotate bool) bool {
	if len(comparisons) == 0 {
		fmt.Fprintln(w, "No shadow groups: nothing in compat/suites/registry.generated.json carries \"shadowOf\".")
		return true
	}
	clear := true
	for _, c := range comparisons {
		agreed := c.of(shadowAgree)
		diverged := c.of(shadowDiverge)
		debt := c.of(shadowDebtClosed)
		idle := c.of(shadowUnexercised)
		fmt.Fprintf(w, "%s vs %s: %d pair(s) — %d agreed, %d diverged, %d parity debt closed, %d not exercised\n",
			c.Shadow, c.Native, len(c.Pairs), len(agreed), len(diverged), len(debt), len(idle))
		if len(c.Pairs) == 0 {
			// Nothing ran, and nothing was even registered to run. That is not
			// agreement, and reporting it as such is how a flip PR ends up
			// citing a soak that never happened.
			clear = false
			fmt.Fprintln(w, "  no results for either group — the run did not exercise them, so this proves nothing")
			if annotate {
				fmt.Fprintf(w, "::error title=Compat shadow::%s vs %s: neither group reported any result\n", c.Shadow, c.Native)
			}
			continue
		}
		for _, p := range diverged {
			clear = false
			fmt.Fprintf(w, "  diverged     %-12s %-28s native=%s shadow=%s\n",
				p.Suite, p.Test, orNoResult(p.Native), orNoResult(p.Shadow))
			if annotate {
				fmt.Fprintf(w, "::error title=Compat shadow::%s/%s: %s answered %s, %s answered %s\n",
					p.Suite, p.Test, c.Native, orNoResult(p.Native), c.Shadow, orNoResult(p.Shadow))
			}
		}
		for _, p := range debt {
			fmt.Fprintf(w, "  debt closed  %-12s %-28s the suite never implemented %s; the port runs it\n",
				p.Suite, p.Test, c.Native)
		}
		for _, p := range idle {
			fmt.Fprintf(w, "  not run      %-12s %-28s neither half was exercised (%s)\n",
				p.Suite, p.Test, orNoReason(p.NativeReason, p.ShadowReason))
		}
	}
	if clear {
		fmt.Fprintln(w, "No divergence. See docs/plans/compat-coverage-modelgen.md §3.11 for what the flip PR does next; a pair marked \"not run\" is not evidence for it.")
	} else {
		fmt.Fprintln(w, "A divergence blocks the deletion of the native code, never the gate. Triage each as an IR expressiveness gap or a latent bug in one copy (§3.11 step 3).")
	}
	return clear
}

// orNoReason is the reason to print for a pair neither half ran, preferring
// whichever half said something: a cascade skip names the setup that fell over,
// which is the whole of what a reader needs.
func orNoReason(native, shadow string) string {
	if native != "" {
		return native
	}
	if shadow != "" {
		return shadow
	}
	return "no reason given"
}

func orNoResult(status compat.Status) string {
	if status == "" {
		return "<no result>"
	}
	return string(status)
}

// compareShadowFile is the --compare-shadow entry point.
func compareShadowFile(generatedRegistryPath, resultsPath string, annotate bool, w io.Writer) error {
	gen, err := readGeneratedRegistry(generatedRegistryPath)
	if err != nil {
		return err
	}
	report, err := readRunReportFile(resultsPath)
	if err != nil {
		return err
	}
	comparisons := compareShadows(gen, report)
	if !writeShadowReport(w, comparisons, annotate) {
		return fmt.Errorf("%d shadow group(s) did not reproduce their native predecessor exactly", countDiverged(comparisons))
	}
	return nil
}

func countDiverged(comparisons []shadowComparison) int {
	n := 0
	for _, c := range comparisons {
		if len(c.Pairs) == 0 || len(c.divergences()) > 0 {
			n++
		}
	}
	return n
}
