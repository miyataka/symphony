//go:build !unix

package orchestrator

func supportsProcessGroupTests() bool {
	return false
}

func cleanupProcess(pid int) {
}

func processExists(pid int) bool {
	return false
}
