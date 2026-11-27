package dil_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"dif/api"
)

// TestManifestDerivesForEveryFlow derives the capability manifest of every
// flow of the repository: those in testdata and the programs in api/testdata.
// Every step declares its capabilities, each with a known kind and a target;
// a policy that grants everything permits every flow, and an empty one
// (the sandbox) checks them all without failing.
func TestManifestDerivesForEveryFlow(t *testing.T) {
	t.Chdir(workdir)
	kinds := []string{"net.connect", "net.listen", "fs.read", "fs.write", "env"}
	everything := map[string][]string{}
	for _, k := range kinds {
		everything[k] = []string{"*"}
	}
	all, err := api.NewPolicy(everything)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := api.NewPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	steps := 0
	for _, f := range append(flowFiles(t), programFiles(t)...) {
		p, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Name: f, Path: path(f)}}, api.ProgramLimits{})
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		info := p.Info()
		for _, flow := range info.Flows {
			for _, s := range flow.Flow.Steps {
				steps++
				if s.CapabilitiesUndeclared {
					t.Errorf("%s: flow %s, step %s (%s): no capabilities declared", f, flow.Flow.ID, s.ID, s.Step)
				}
				for _, c := range s.Capabilities {
					if !slices.Contains(kinds, c.Kind) || c.Target == "" {
						t.Errorf("%s: flow %s, step %s: capability %q", f, flow.Flow.ID, s.ID, c)
					}
					key := c.Kind
					if c.Dynamic {
						key += " (dynamic)"
					}
					counts[key]++
				}
			}
		}
		if ds := info.CheckPolicy(all); len(ds) != 0 {
			t.Errorf("%s: a policy that grants everything refuses: %v", f, ds)
		}
		p.Info().CheckPolicy(sandbox)
	}
	var summary []string
	for k, n := range counts {
		summary = append(summary, k+": "+itoa(n))
	}
	sort.Strings(summary)
	t.Logf("%d steps; capabilities: %s", steps, strings.Join(summary, ", "))
}
