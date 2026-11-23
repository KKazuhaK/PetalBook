//go:build race

package space

// raceDetector says the tests run under the race detector, where reading and
// writing YAML costs many times what it does otherwise.
const raceDetector = true
