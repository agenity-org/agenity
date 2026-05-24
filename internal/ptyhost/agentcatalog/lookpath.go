package agentcatalog

import "os/exec"

func execLookPathReal(name string) (string, error) {
	return exec.LookPath(name)
}
