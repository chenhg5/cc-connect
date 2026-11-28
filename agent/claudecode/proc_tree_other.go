//go:build !windows

package claudecode

// noopProcessTreeProbe is the process table probe for platforms without
// process-tree enumeration.
//
// On Unix the whole process group is terminated with a single signal (see
// proc_unix.go), which reaps every descendant that stayed in the group, so
// there is no orphan tree left to verify.
type noopProcessTreeProbe struct{}

func (noopProcessTreeProbe) descendants(int) []processRef { return nil }

func (noopProcessTreeProbe) alive(int) bool { return false }

func (noopProcessTreeProbe) identity(int) string { return "" }

func (noopProcessTreeProbe) kill(int) error { return nil }

// newProcessTreeProbe returns the probe for the current platform.
func newProcessTreeProbe() processTreeProbe {
	return noopProcessTreeProbe{}
}
