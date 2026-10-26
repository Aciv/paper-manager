//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type InstanceLock struct {
	file    *os.File
	overlap windows.Overlapped
}

func Acquire(root string) (*InstanceLock, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return nil, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	err = windows.SetNamedSecurityInfo(
		root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil,
	)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, "launcher.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	lock := &InstanceLock{file: f}
	err = windows.LockFileEx(
		windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &lock.overlap,
	)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("another Launcher owns this data directory")
	}
	return lock, nil
}
func (l *InstanceLock) Close() error { return l.file.Close() }

func lockBundleFile(f *os.File) (*InstanceLock, error) {
	overlap := windows.Overlapped{Offset: 0xfffffffe, OffsetHigh: 0x7fffffff}
	lock := &InstanceLock{file: f, overlap: overlap}
	err := windows.LockFileEx(
		windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &lock.overlap,
	)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("runtime is in use: %w", err)
	}
	return lock, nil
}

var jobs sync.Map // cmd pointer -> private kill-on-close job
func prepareProcess(cmd *exec.Cmd) (func(), func(), error) {
	// Services belong to the Launcher job, not its console. Closing the console
	// must give the Launcher a chance to shut them down instead of broadcasting
	// CTRL_CLOSE_EVENT to PostgreSQL and Python at the same time.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return nil, nil, err
	}
	jobs.Store(cmd, job)
	return func() { jobs.Delete(cmd); windows.CloseHandle(job) }, func() { _ = windows.TerminateJobObject(job, 1) }, nil
}
func ownProcess(cmd *exec.Cmd) error {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	job, _ := jobs.Load(cmd)
	return windows.AssignProcessToJobObject(job.(windows.Handle), handle)
}
func ordinaryUser() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("start Launcher as a normal Windows user, without Run as administrator")
	}
	return nil
}
func nativePath(path string) (string, error) {
	ascii := true
	for _, r := range path {
		if r > 127 {
			ascii = false
			break
		}
	}
	if ascii {
		return path, nil
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n >= uint32(len(buf)) {
		return "", fmt.Errorf("PostgreSQL needs an ASCII path on this volume; choose ASCII installation/data directories")
	}
	result := windows.UTF16ToString(buf[:n])
	for _, r := range result {
		if r > 127 {
			return "", fmt.Errorf("PostgreSQL needs an ASCII path on this volume; choose ASCII installation/data directories")
		}
	}
	return result, nil
}
