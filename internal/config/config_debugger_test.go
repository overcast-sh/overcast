package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
)

// TestLoad_debuggerDefaults pins the debugger's off-by-default posture and the
// two derived defaults: the listen address follows the resolved OVERCAST_LISTEN
// host, and the port range starts where Node.js editors expect it.
func TestLoad_debuggerDefaults(t *testing.T) {
	// Given: nothing debugger-related is set, and OVERCAST_LISTEN resolves
	// to loopback (native run)
	clearEnv(t)
	t.Setenv("OVERCAST_DATA_DIR", t.TempDir())

	// When: we load config
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Then: every flag is off, and the derived defaults follow the host
	if cfg.Debugger || cfg.LambdaDebugger || cfg.ECSDebugger {
		t.Errorf("debugger flags: expected all off, got umbrella=%v lambda=%v ecs=%v",
			cfg.Debugger, cfg.LambdaDebugger, cfg.ECSDebugger)
	}
	if cfg.DebuggerListen != cfg.Host {
		t.Errorf("DebuggerListen = %q, want the resolved host %q", cfg.DebuggerListen, cfg.Host)
	}
	if cfg.DebuggerPorts != [2]int{9229, 9329} {
		t.Errorf("DebuggerPorts = %v, want [9229 9329]", cfg.DebuggerPorts)
	}
	if cfg.DebuggerTimeout != config.DebuggerTimeoutAttached {
		t.Errorf("DebuggerTimeout = %q, want attached", cfg.DebuggerTimeout)
	}
	if cfg.DebuggerWaitTimeout != 120*time.Second {
		t.Errorf("DebuggerWaitTimeout = %s, want 120s", cfg.DebuggerWaitTimeout)
	}
}

// TestLoad_debuggerListenFollowsContainerisedHost verifies the listen default
// is the same decision OVERCAST_LISTEN makes (#761), not a second detection:
// containerised binds every interface so Docker's -p publishing reaches the
// debug ports.
func TestLoad_debuggerListenFollowsContainerisedHost(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		source string
		want   string
	}{
		{name: "containerised default", source: "image", want: "0.0.0.0"},
		{name: "native default", want: "127.0.0.1"},
		{name: "explicit OVERCAST_LISTEN", listen: "10.0.0.5,127.0.0.1", want: "10.0.0.5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the run is containerised, native, or explicitly bound
			clearEnv(t)
			t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
			if tc.source != "" {
				t.Setenv("OVERCAST_DATA_DIR_SOURCE", tc.source)
			}
			if tc.listen != "" {
				t.Setenv("OVERCAST_LISTEN", tc.listen)
			}

			// When: we load config
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			// Then: the debug ports bind where the API does
			if cfg.DebuggerListen != tc.want {
				t.Errorf("DebuggerListen = %q, want %q", cfg.DebuggerListen, tc.want)
			}
		})
	}
}

// TestLoad_debuggerInheritance mirrors TestLoad_hotReloadInheritance: the
// per-service variables inherit the umbrella when unset and beat it when set,
// in both directions.
func TestLoad_debuggerInheritance(t *testing.T) {
	tests := []struct {
		name                  string
		umbrella, lambda, ecs string
		wantLambda, wantECS   bool
	}{
		{name: "nothing set"},
		{name: "umbrella on", umbrella: "true", wantLambda: true, wantECS: true},
		{name: "lambda only", lambda: "true", wantLambda: true},
		{name: "ecs only", ecs: "true", wantECS: true},
		{name: "umbrella off, ecs on", umbrella: "false", ecs: "true", wantECS: true},
		{name: "umbrella on, lambda opted out", umbrella: "true", lambda: "false", wantECS: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: some combination of umbrella and per-service flags
			clearEnv(t)
			t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
			if tc.umbrella != "" {
				t.Setenv("OVERCAST_DEBUGGER", tc.umbrella)
			}
			if tc.lambda != "" {
				t.Setenv("OVERCAST_LAMBDA_DEBUGGER", tc.lambda)
			}
			if tc.ecs != "" {
				t.Setenv("OVERCAST_ECS_DEBUGGER", tc.ecs)
			}

			// When: we load config
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			// Then: each service resolves to its own value, else the umbrella
			if cfg.LambdaDebugger != tc.wantLambda {
				t.Errorf("LambdaDebugger = %v, want %v", cfg.LambdaDebugger, tc.wantLambda)
			}
			if cfg.ECSDebugger != tc.wantECS {
				t.Errorf("ECSDebugger = %v, want %v", cfg.ECSDebugger, tc.wantECS)
			}
		})
	}
}

// TestLoad_debuggerSettings covers the explicit forms of the three value
// settings and the shapes each refuses. Invalid values fail startup naming the
// variable, like every other typed setting here.
func TestLoad_debuggerSettings(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantListen  string
		wantPorts   [2]int
		wantTimeout config.DebuggerTimeoutPolicy
		wantWait    time.Duration
		wantErr     string
	}{
		{
			name:        "explicit listen",
			env:         map[string]string{"OVERCAST_DEBUGGER_LISTEN": " 0.0.0.0 "},
			wantListen:  "0.0.0.0",
			wantPorts:   [2]int{9229, 9329},
			wantTimeout: config.DebuggerTimeoutAttached,
		},
		{
			name:    "listen with a list",
			env:     map[string]string{"OVERCAST_DEBUGGER_LISTEN": "127.0.0.1,0.0.0.0"},
			wantErr: "OVERCAST_DEBUGGER_LISTEN",
		},
		{
			name:        "port range",
			env:         map[string]string{"OVERCAST_DEBUGGER_PORTS": "5005-5010"},
			wantListen:  "127.0.0.1",
			wantPorts:   [2]int{5005, 5010},
			wantTimeout: config.DebuggerTimeoutAttached,
		},
		{
			name:        "single port",
			env:         map[string]string{"OVERCAST_DEBUGGER_PORTS": "9229"},
			wantListen:  "127.0.0.1",
			wantPorts:   [2]int{9229, 9229},
			wantTimeout: config.DebuggerTimeoutAttached,
		},
		{
			name:    "reversed range",
			env:     map[string]string{"OVERCAST_DEBUGGER_PORTS": "9329-9229"},
			wantErr: "OVERCAST_DEBUGGER_PORTS",
		},
		{
			name:    "port out of range",
			env:     map[string]string{"OVERCAST_DEBUGGER_PORTS": "0-70000"},
			wantErr: "OVERCAST_DEBUGGER_PORTS",
		},
		{
			name:    "ports not numeric",
			env:     map[string]string{"OVERCAST_DEBUGGER_PORTS": "nine-ten"},
			wantErr: "OVERCAST_DEBUGGER_PORTS",
		},
		{
			name:        "timeout paused, any case",
			env:         map[string]string{"OVERCAST_DEBUGGER_TIMEOUT": "Paused"},
			wantListen:  "127.0.0.1",
			wantPorts:   [2]int{9229, 9329},
			wantTimeout: config.DebuggerTimeoutPaused,
		},
		{
			name:        "timeout strict",
			env:         map[string]string{"OVERCAST_DEBUGGER_TIMEOUT": "strict"},
			wantListen:  "127.0.0.1",
			wantPorts:   [2]int{9229, 9329},
			wantTimeout: config.DebuggerTimeoutStrict,
		},
		{
			name:    "timeout unknown",
			env:     map[string]string{"OVERCAST_DEBUGGER_TIMEOUT": "never"},
			wantErr: "OVERCAST_DEBUGGER_TIMEOUT",
		},
		{
			name:        "wait timeout",
			env:         map[string]string{"OVERCAST_DEBUGGER_WAIT_TIMEOUT": "5s"},
			wantListen:  "127.0.0.1",
			wantPorts:   [2]int{9229, 9329},
			wantTimeout: config.DebuggerTimeoutAttached,
			wantWait:    5 * time.Second,
		},
		{
			name:    "wait timeout not a duration",
			env:     map[string]string{"OVERCAST_DEBUGGER_WAIT_TIMEOUT": "soon"},
			wantErr: "OVERCAST_DEBUGGER_WAIT_TIMEOUT",
		},
		{
			name:    "wait timeout zero",
			env:     map[string]string{"OVERCAST_DEBUGGER_WAIT_TIMEOUT": "0s"},
			wantErr: "OVERCAST_DEBUGGER_WAIT_TIMEOUT",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a native run with one debugger setting spelled explicitly
			clearEnv(t)
			t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			// When: we load config
			cfg, err := config.Load()

			// Then: the value is parsed, or startup fails naming the variable
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error naming %s, got none", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not name %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.DebuggerListen != tc.wantListen {
				t.Errorf("DebuggerListen = %q, want %q", cfg.DebuggerListen, tc.wantListen)
			}
			if cfg.DebuggerPorts != tc.wantPorts {
				t.Errorf("DebuggerPorts = %v, want %v", cfg.DebuggerPorts, tc.wantPorts)
			}
			if cfg.DebuggerTimeout != tc.wantTimeout {
				t.Errorf("DebuggerTimeout = %q, want %q", cfg.DebuggerTimeout, tc.wantTimeout)
			}
			wantWait := tc.wantWait
			if wantWait == 0 {
				wantWait = 120 * time.Second
			}
			if cfg.DebuggerWaitTimeout != wantWait {
				t.Errorf("DebuggerWaitTimeout = %s, want %s", cfg.DebuggerWaitTimeout, wantWait)
			}
		})
	}
}

// TestParseDebuggerTimeoutPolicy_roundTrip pins that String returns the
// spelling Parse accepts, since the descriptor renders the policy back to the
// console and the docs quote it.
func TestParseDebuggerTimeoutPolicy_roundTrip(t *testing.T) {
	for _, p := range []config.DebuggerTimeoutPolicy{
		config.DebuggerTimeoutAttached, config.DebuggerTimeoutPaused, config.DebuggerTimeoutStrict,
	} {
		// Given: a policy rendered with String
		// When: it is parsed again
		got, err := config.ParseDebuggerTimeoutPolicy(p.String())

		// Then: the same policy comes back
		if err != nil || got != p {
			t.Errorf("Parse(%q) = %q, %v; want %q", p.String(), got, err, p)
		}
	}
}
