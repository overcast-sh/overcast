//go:build dev

package capabilities_test

import (
	"testing"

	"github.com/overcast-sh/overcast/internal/capabilities"
)

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := capabilities.NewRegistry()
	r.Register(
		capabilities.Capability{Service: "test", Operation: "CreateFoo", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "test", Operation: "DeleteFoo", Status: capabilities.StatusUnsupported},
	)
	if c, ok := r.Lookup("test", "CreateFoo"); !ok || c.Status != capabilities.StatusSupported {
		t.Fatalf("expected CreateFoo as Supported: ok=%v status=%v", ok, c.Status)
	}
	if _, ok := r.Lookup("test", "Missing"); ok {
		t.Fatal("expected Missing to not be found")
	}
	all := r.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 capabilities, got %d", len(all))
	}
	forSvc := r.ForService("test")
	if len(forSvc) != 2 {
		t.Fatalf("expected 2 capabilities for service test, got %d", len(forSvc))
	}
}

func TestRegistryUpdate(t *testing.T) {
	r := capabilities.NewRegistry()
	r.Register(capabilities.Capability{Service: "test", Operation: "Op1", Status: capabilities.StatusWIP})
	r.Register(capabilities.Capability{Service: "test", Operation: "Op1", Status: capabilities.StatusPartial})
	c, ok := r.Lookup("test", "Op1")
	if !ok {
		t.Fatal("expected Op1 to be found")
	}
	if c.Status != capabilities.StatusPartial {
		t.Fatalf("expected StatusPartial after update, got %v", c.Status)
	}
	if len(r.All()) != 1 {
		t.Fatalf("expected 1 capability after update (no duplicate), got %d", len(r.All()))
	}
}

func TestStatusString(t *testing.T) {
	cases := map[capabilities.Status]string{
		capabilities.StatusSupported:   "Supported",
		capabilities.StatusPartial:     "Partial",
		capabilities.StatusWIP:         "WIP",
		capabilities.StatusUnsupported: "Unsupported",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestRegistryForServiceEmpty(t *testing.T) {
	r := capabilities.NewRegistry()
	r.Register(capabilities.Capability{Service: "sqs", Operation: "CreateQueue", Status: capabilities.StatusSupported})
	if got := r.ForService("s3"); len(got) != 0 {
		t.Fatalf("expected empty for unknown service, got %v", got)
	}
}

// TestAllCapabilitiesEmulatorOnlySurvivesGeneration pins the one property the
// generated snapshot can lose silently: capgen parses EmulatorOnly out of a
// capabilities_dev.go declaration and writes it back into all.gen.go, so a
// missing case in either half would drop the flag and put the row back into
// the AWS operation counts without failing a build.
func TestAllCapabilitiesEmulatorOnlySurvivesGeneration(t *testing.T) {
	var found bool
	for _, c := range capabilities.AllCapabilities {
		if c.Service != "cloudfront" || c.Operation != "ProxyRequest" {
			continue
		}
		found = true
		if !c.EmulatorOnly {
			t.Error("cloudfront/ProxyRequest is not marked EmulatorOnly in all.gen.go; run: make generate-caps")
		}
		if c.DocsURL != "" {
			t.Errorf("cloudfront/ProxyRequest declares DocsURL %q; an operation AWS does not model has no AWS docs page", c.DocsURL)
		}
	}
	if !found {
		t.Fatal("cloudfront/ProxyRequest is missing from AllCapabilities")
	}
}
