package lambda

// RuntimeImage returns the official Lambda base image Overcast runs a runtime
// on, and false when the runtime is unknown or Overcast cannot execute it.
//
// It exists for the packages that run Lambda base images without being the
// Lambda service — Lambda Web Functions start their Node.js servers from the
// same catalog entries, so a runtime resolves to one image everywhere.
func RuntimeImage(runtimeID string) (string, bool) {
	image, ok := activeRuntimes[runtimeID]
	return image, ok
}
