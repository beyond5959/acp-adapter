package appserver

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// ProcessConfig configures how codex app-server is spawned.
type ProcessConfig struct {
	Command string
	Args    []string
	Env     []string
	Stderr  io.Writer
}

// Process wraps a running app-server subprocess.
type Process struct {
	cmd        *exec.Cmd
	stdinPipe  io.WriteCloser
	stdoutPipe io.ReadCloser

	waitCh    chan error
	closeOnce sync.Once
}

// StartProcess starts a child process and returns pipe handles.
func StartProcess(ctx context.Context, cfg ProcessConfig) (*Process, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("app server command is empty")
	}

	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Env = append(os.Environ(), cfg.Env...)
	if cfg.Stderr != nil {
		cmd.Stderr = cfg.Stderr
	} else {
		cmd.Stderr = os.Stderr
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create app-server stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create app-server stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start app-server process: %w", err)
	}

	process := &Process{
		cmd:        cmd,
		stdinPipe:  stdinPipe,
		stdoutPipe: stdoutPipe,
		waitCh:     make(chan error, 1),
	}

	go func() {
		process.waitCh <- cmd.Wait()
		close(process.waitCh)
	}()

	return process, nil
}

// Stdin returns the writable stream for child stdin.
func (p *Process) Stdin() io.Writer {
	return p.stdinPipe
}

// Stdout returns the readable stream for child stdout.
func (p *Process) Stdout() io.Reader {
	return p.stdoutPipe
}

// WaitChan returns a channel completed when child exits.
func (p *Process) WaitChan() <-chan error {
	return p.waitCh
}

// Close terminates process and waits for exit.
func (p *Process) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		if p.stdinPipe != nil {
			_ = p.stdinPipe.Close()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		if p.waitCh != nil {
			if err, ok := <-p.waitCh; ok {
				closeErr = err
			}
		}
	})
	return closeErr
}
