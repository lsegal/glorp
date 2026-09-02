package browser

import "os/exec"

// Supervisor starts and stops the browser subprocess. glorp owns every process
// it spawns -- they run in their own process group, are tracked so the whole
// tree is terminated before glorp exits, and are guarded by the kernel should
// glorp die without cleaning up -- but that ownership is the root package's
// policy rather than anything the browser driver knows about, so it is handed
// in with the rest of the Config.
type Supervisor interface {
	// Start starts cmd as an owned subprocess.
	Start(cmd *exec.Cmd) error
	// Stop terminates a started subprocess and reaps it.
	Stop(cmd *exec.Cmd) error
	// Run starts a subprocess and waits for it to finish.
	Run(cmd *exec.Cmd) error
}

// directSupervisor is the fallback used by a Config that names no supervisor,
// so a test can launch a browser without the root package's tracking.
type directSupervisor struct{}

func (directSupervisor) Start(cmd *exec.Cmd) error { return cmd.Start() }

func (directSupervisor) Run(cmd *exec.Cmd) error { return cmd.Run() }

func (directSupervisor) Stop(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil
}

// supervisor returns the supervisor to spawn this browser's process with.
func (c Config) supervisor() Supervisor {
	if c.Supervisor == nil {
		return directSupervisor{}
	}
	return c.Supervisor
}
