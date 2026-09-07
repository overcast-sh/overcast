package debugger

// dap is Python's debugpy, speaking the Debug Adapter Protocol. Nothing can
// be injected: the Lambda Python image ships no debugpy, so the handler has
// to call debugpy.listen itself. The editor template carries that two-line
// snippet, reading the port from OVERCAST_DEBUG_PORT, which the package sets
// for every protocol. Shipping debugpy through the init volume is listed as
// later work in the plan.
type dap struct{}

const dapName = "dap"

func (dap) Name() string                  { return dapName }
func (dap) Runtimes() []string            { return []string{"python"} }
func (dap) Inject(map[string]string, int) {}

// Detect always fails: debugpy is started from code, not from a variable
// Overcast could read.
func (dap) Detect(map[string]string) (int, bool) { return 0, false }

func (dap) Editors() []EditorTemplate { return dapEditors }
