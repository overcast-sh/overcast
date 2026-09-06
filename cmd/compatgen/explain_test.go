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
		"28 of 41 modeled operations",
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
