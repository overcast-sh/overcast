//go:build dev

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExplain_rendersEveryLanguage(t *testing.T) {
	_, gen := generateFixture(t)
	g, tc, ok := gen.scenario.findTest("widgets-gen-widget", "CreateWidget")
	if !ok {
		t.Fatal("fixture has no CreateWidget")
	}
	for _, lang := range rendererNames() {
		t.Run(lang, func(t *testing.T) {
			out := renderers[lang](fixtureRenderEnv(gen), gen.scenario, g, tc)
			// Operation names are spelled per language (get_widget, getWidget,
			// GetWidget), so compare with case and separators folded.
			folded := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(out))
			for _, want := range []string{"widgetsgenwidget/createwidget", "createwidget", "getwidget", "listwidgets", "assert"} {
				if !strings.Contains(folded, want) {
					t.Errorf("%s rendering lacks %q:\n%s", lang, want, out)
				}
			}
			if strings.Contains(out, "$ref") || strings.Contains(out, "$name") {
				t.Errorf("%s rendering leaks IR syntax:\n%s", lang, out)
			}
		})
	}
}

// TestExplain_rendersAWrappedBind proves every rendering spells a list-shaped
// member bound from a scalar export (#1923) as a list holding the context
// value, in that backend's own syntax, rather than leaking the IR's `$ref` or
// sending the reference bare. Rust is the odd one out and correctly so: a
// smithy-rs fluent setter appends one element at a time, so the list is the
// repeated call and the binder key carries the index.
func TestExplain_rendersAWrappedBind(t *testing.T) {
	_, gen := generateFixture(t)
	g, tc, ok := gen.scenario.findTest("widgets-gen-hub", "TagHub")
	if !ok {
		t.Fatal("fixture has no TagHub")
	}
	want := map[string]string{
		"cli":    `"HubNames": [$HUB_NAME]`,
		"dotnet": `request.HubNames = [b.Bind<string>("HubNames", Val.Ref("hub.name"))];`,
		"go":     `in.HubNames = []string{scenario.Bind[string](b, "HubNames", scenario.Ref("hub.name"))}`,
		"java":   `.hubNames(List.of(b.string("HubNames", Values.ref("hub.name"))))`,
		"node":   `HubNames: [ctx["hub.name"]]`,
		"python": `HubNames=[ctx["hub.name"]]`,
		"rust":   `.hub_names(b.string("HubNames[0]")?)`,
	}
	for _, lang := range rendererNames() {
		t.Run(lang, func(t *testing.T) {
			out := renderers[lang](fixtureRenderEnv(gen), gen.scenario, g, tc)
			if !strings.Contains(out, want[lang]) {
				t.Errorf("%s rendering lacks %q:\n%s", lang, want[lang], out)
			}
			if strings.Contains(out, "$ref") {
				t.Errorf("%s rendering leaks IR syntax:\n%s", lang, out)
			}
		})
	}
}

func TestExplain_readsTheCommittedScenario(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", repoRoot, "-explain", "sqs-gen-queue/DeleteQueue", "-lang", "python"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"client.delete_queue(", "AWS.SimpleQueueService.NonExistentQueue", "QueueDoesNotExist", "boto3.client(\"sqs\""} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	if code := run([]string{"-root", repoRoot, "-explain", "sqs-gen-queue/Nope"}, &stdout, &stderr); code != 1 {
		t.Fatalf("unknown test accepted: code=%d", code)
	}
	if code := run([]string{"-root", repoRoot, "-explain", "sqs-gen-queue/DeleteQueue", "-lang", "cobol"}, &stdout, &stderr); code != 1 {
		t.Fatalf("unknown language accepted: code=%d", code)
	}
}

func TestReport_listsCoverageRefusalsAndSamples(t *testing.T) {
	_, gen := generateFixture(t)
	var out bytes.Buffer
	writeReport(&out, gen, 2)
	report := out.String()
	for _, want := range []string{
		"32 of 45 modeled operations",
		"| RotateWidget | widgets-gen-probe | `never-probe` |",
		"| SetWidgetSize | widgets-gen-probe | `update-without-mutable` |",
		"### Automatic name-match bindings",
		"None — every bound member",
		"### Sampled scenarios (2, seed 1113)",
		"```python",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	var again bytes.Buffer
	writeReport(&again, gen, 2)
	if again.String() != report {
		t.Fatal("the report is not deterministic")
	}
}

// staticModel is the renderEnv loader for a test that already holds the model
// it wants every lookup to answer with.
func staticModel(model *serviceModel) func(string) (*serviceModel, error) {
	return func(string) (*serviceModel, error) { return model, nil }
}
