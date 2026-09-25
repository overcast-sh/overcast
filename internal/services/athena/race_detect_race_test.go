//go:build race

package athena

// raceDetectorEnabled is true when the test binary was built with `go test
// -race`, whose instrumentation makes every operation several times slower.
// A test that pushes millions of rows through the result path pushes a
// tenth of them under it, too few for its heap bound to prove anything, so
// it checks only the output there.
const raceDetectorEnabled = true
