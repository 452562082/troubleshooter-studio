package browserverify

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

const commandOutputDrainTimeout = 2 * time.Second

type commandOutputAttacher func(*exec.Cmd) (*ownedCommandOutputs, error)

type ownedCommandOutputs struct {
	stdoutRead  *os.File
	stdoutWrite *os.File
	stderrRead  *os.File
	stderrWrite *os.File
	closeWrite  func(*os.File) error
}

func attachOwnedCommandOutputs(command *exec.Cmd) (*ownedCommandOutputs, error) {
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return nil, err
	}
	outputs := &ownedCommandOutputs{
		stdoutRead: stdoutRead, stdoutWrite: stdoutWrite,
		stderrRead: stderrRead, stderrWrite: stderrWrite,
	}
	command.Stdout = stdoutWrite
	command.Stderr = stderrWrite
	return outputs, nil
}

func (outputs *ownedCommandOutputs) childStarted() error {
	stdoutErr := outputs.closeWriter(outputs.stdoutWrite)
	outputs.stdoutWrite = nil
	stderrErr := outputs.closeWriter(outputs.stderrWrite)
	outputs.stderrWrite = nil
	return errors.Join(stdoutErr, stderrErr)
}

func (outputs *ownedCommandOutputs) closeWriter(file *os.File) error {
	if outputs.closeWrite != nil {
		return outputs.closeWrite(file)
	}
	return file.Close()
}

func (outputs *ownedCommandOutputs) copyTo(stdout, stderr io.Writer) (<-chan error, <-chan error) {
	stdoutDone := make(chan error, 1)
	stderrDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, outputs.stdoutRead)
		stdoutDone <- err
	}()
	go func() {
		_, err := io.Copy(stderr, outputs.stderrRead)
		stderrDone <- err
	}()
	return stdoutDone, stderrDone
}

func (outputs *ownedCommandOutputs) waitCopies(stdoutDone, stderrDone <-chan error) error {
	timer := time.NewTimer(commandOutputDrainTimeout)
	defer timer.Stop()
	timeout := timer.C
	var stdoutErr, stderrErr error
	for stdoutDone != nil || stderrDone != nil {
		select {
		case stdoutErr = <-stdoutDone:
			stdoutDone = nil
		case stderrErr = <-stderrDone:
			stderrDone = nil
		case <-timeout:
			_ = outputs.closeReaders()
			timeout = nil
		}
	}
	return errors.Join(stdoutErr, stderrErr)
}

func (outputs *ownedCommandOutputs) closeReaders() error {
	stdoutErr := outputs.stdoutRead.Close()
	stderrErr := outputs.stderrRead.Close()
	return errors.Join(stdoutErr, stderrErr)
}

func (outputs *ownedCommandOutputs) closeAll() error {
	var stdoutWriteErr, stderrWriteErr error
	if outputs.stdoutWrite != nil {
		stdoutWriteErr = outputs.stdoutWrite.Close()
		outputs.stdoutWrite = nil
	}
	if outputs.stderrWrite != nil {
		stderrWriteErr = outputs.stderrWrite.Close()
		outputs.stderrWrite = nil
	}
	return errors.Join(stdoutWriteErr, stderrWriteErr, outputs.closeReaders())
}
