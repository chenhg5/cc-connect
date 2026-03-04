package core

// Compile-time interface checks for WorkDirOverrider.
// These are tested via the agent-specific test files, but we verify
// the interface shape here.

func ExampleWorkDirOverrider() {
	// This function exists to verify the interface compiles correctly.
	var _ WorkDirOverrider = (*mockWorkDirAgent)(nil)
}

type mockWorkDirAgent struct {
	workDir         string
	overrideWorkDir string
}

func (m *mockWorkDirAgent) SetWorkDir(dir string)  { m.overrideWorkDir = dir }
func (m *mockWorkDirAgent) ResetWorkDir()           { m.overrideWorkDir = "" }
