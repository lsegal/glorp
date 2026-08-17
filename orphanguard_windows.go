package main

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// orphanGuard names the kernel mechanism this platform uses, for the startup
// diagnostic and for tests.
const orphanGuard = "job object"

// ownedJob holds every subprocess glorp starts. The job is configured to kill
// its members when its last handle closes, which happens when glorp's process
// object is destroyed — including when glorp is terminated outright and runs no
// cleanup of its own (issue #264). Unlike Linux's parent-death signal, job
// membership is inherited, so grandchildren are covered too.
var ownedJob struct {
	once   sync.Once
	handle windows.Handle
	err    error
}

func ownedJobObject() (windows.Handle, error) {
	ownedJob.once.Do(func() {
		handle, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			ownedJob.err = err
			return
		}
		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(
			handle,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)),
			uint32(unsafe.Sizeof(limits)),
		); err != nil {
			_ = windows.CloseHandle(handle)
			ownedJob.err = err
			return
		}
		// The handle is deliberately never closed: it is what keeps the job
		// alive, and its destruction with the process is the guarantee.
		ownedJob.handle = handle
	})
	return ownedJob.handle, ownedJob.err
}

// guardOrphanedProcess has nothing to do before the child starts; Windows can
// only put a running process into a job.
func guardOrphanedProcess(*exec.Cmd) {}

// adoptOrphanedProcess puts a freshly started child into glorp's job object. A
// child that exits in the moment between starting and being assigned is not an
// orphan anyway, so a failure here is not worth reporting; the userspace
// cleanup paths still cover every exit glorp can observe.
func adoptOrphanedProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	job, err := ownedJobObject()
	if err != nil {
		return
	}
	const access = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	handle, err := windows.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	_ = windows.AssignProcessToJobObject(job, handle)
}
