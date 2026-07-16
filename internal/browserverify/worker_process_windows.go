//go:build windows

package browserverify

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type workerProcessController struct{}

const windowsJobWrapperArgument = "--tshoot-browser-job-wrapper"

func init() {
	if len(os.Args) >= 3 && os.Args[1] == windowsJobWrapperArgument {
		os.Exit(runWindowsJobWrappedCommand(os.Args[2], os.Args[3:]))
	}
}

func configureWorkerProcess(command *exec.Cmd) *workerProcessController {
	executable, err := os.Executable()
	if err != nil {
		command.Path = ""
		command.Args = nil
		return &workerProcessController{}
	}
	originalPath := command.Path
	originalArgs := append([]string(nil), command.Args[1:]...)
	command.Path = executable
	command.Args = append([]string{executable, windowsJobWrapperArgument, originalPath}, originalArgs...)
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Kill()
	}
	return &workerProcessController{}
}

func (*workerProcessController) kill(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return command.Process.Kill()
}

func (*workerProcessController) finish() error { return nil }

func runWindowsJobWrappedCommand(executable string, args []string) int {
	job, err := createKillOnCloseJob()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "browser process containment failed: %v\n", err)
		return 1
	}
	defer windows.CloseHandle(job)

	command := exec.Command(executable, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := command.Start(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "contained browser process start failed: %v\n", err)
		return 1
	}
	if err := assignAndResumeWindowsProcess(job, uint32(command.Process.Pid)); err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		_, _ = fmt.Fprintf(os.Stderr, "browser process containment failed: %v\n", err)
		return 1
	}
	if err := command.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(os.Stderr, "contained browser process wait failed: %v\n", err)
		return 1
	}
	return 0
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func assignAndResumeWindowsProcess(job windows.Handle, processID uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, processID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return err
	}
	return resumeWindowsProcessThreads(processID)
}

func resumeWindowsProcessThreads(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == processID {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return err
			}
			_, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			return errors.Join(resumeErr, closeErr)
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return errors.New("suspended process thread was not found")
			}
			return err
		}
	}
}
