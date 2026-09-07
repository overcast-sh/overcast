package debugger

// passthrough is the protocol for images and custom runtimes: Overcast owns
// the port and the proxy, the user owns the flag. It is where resolution
// lands when a tag asks for a debugger and nothing else matched, so a
// tagged resource always gets a port rather than a shrug.
type passthrough struct{}

const passthroughName = "passthrough"

func (passthrough) Name() string                         { return passthroughName }
func (passthrough) Runtimes() []string                   { return nil }
func (passthrough) Inject(map[string]string, int)        {}
func (passthrough) Detect(map[string]string) (int, bool) { return 0, false }
func (passthrough) Editors() []EditorTemplate            { return passthroughEditors }
