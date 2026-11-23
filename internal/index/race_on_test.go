//go:build race

package index

// raceDetector says the tests run under the race detector, where every query
// costs many times what it does otherwise: the tests that run thousands of
// them run fewer.
const raceDetector = true
