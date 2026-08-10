package read

// SetMaxPointsForTest lowers the response cap and returns a function that
// restores it. Exported to the package's own tests only -- the cap is not
// configurable at runtime, and a hub that let a request raise it would have
// no cap at all.
func SetMaxPointsForTest(n int) func() {
	previous := maxPoints
	maxPoints = n
	return func() { maxPoints = previous }
}
