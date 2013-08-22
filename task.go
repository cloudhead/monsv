package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type task struct {
	path       string
	args       []string
	env        []string
	want       string
	upc, downc chan chan bool
	exitc      chan error
	wstatus    *wstatus
	cmd        *exec.Cmd
	isLog      bool
}

type wstatus struct {
	sync.Mutex

	isRunning bool
	ps        os.ProcessState
	started   time.Time
	exited    time.Time
}

func (t *task) status() (s string) {
	now := time.Now()
	st := t.wstatus

	st.Lock()
	defer st.Unlock()

	if st.isRunning {
		s = fmt.Sprintf("%s: %s (pid %d) %s", t.path, up, t.cmd.Process.Pid, now.Sub(st.started))
	} else if !st.exited.IsZero() {
		s = fmt.Sprintf("%s: %s (%s) %s", t.path, down, st.ps.String(), now.Sub(st.exited))
	} else {
		s = fmt.Sprintf("%s: %s (%s)", t.path, down, st.ps.String())
	}
	return
}

func NewTask(path string, args ...string) *task {
	return &task{
		path:    path,
		args:    args,
		want:    up,
		wstatus: &wstatus{},
		upc:     make(chan chan bool, 1),
		downc:   make(chan chan bool, 1),
		exitc:   make(chan error),
	}
}

func (t *task) spawn(stdin, stdout, stderr *os.File) error {
	t.cmd = exec.Command(t.path, t.args...)
	t.cmd.Env = t.env
	t.cmd.Stdin = stdin
	t.cmd.Stdout = stdout
	t.cmd.Stderr = stderr

	err := t.cmd.Start()
	if err != nil {
		return err
	}

	t.wstatus.Lock()
	t.wstatus.started = time.Now()
	t.wstatus.isRunning = true
	t.wstatus.Unlock()

	return nil
}

func (t *task) signal(sig os.Signal) (bool, error) {
	if t.cmd != nil && t.cmd.Process != nil {
		return true, t.cmd.Process.Signal(sig)
	}
	return false, nil
}

func (t *task) wait(exitc chan<- error) {
	if t.cmd == nil {
		return
	}
	err := t.cmd.Wait()

	t.wstatus.Lock()
	t.wstatus.ps = *t.cmd.ProcessState
	t.wstatus.exited = time.Now()
	t.wstatus.isRunning = false
	t.wstatus.Unlock()

	exitc <- err
}

func (t *task) log(str string) {
	if t.isLog {
		log.Printf("[logger] %s", str)
	} else {
		log.Printf("[worker] %s", str)
	}
}

func (t *task) run(stdin, stdout, stderr *os.File) {
	for {
		callback := <-t.upc

		for t.want = up; t.want != down; callback = nil {
			err := t.spawn(stdin, stdout, stderr)
			if err != nil {
				if callback != nil {
					callback <- false
				}
				log.Printf("error: %s", err)
				time.Sleep(restartWait)
				continue
			}
			if callback != nil {
				callback <- true
			}

			exitc := make(chan error)
			go t.wait(exitc)

			select {
			case err := <-exitc:
				t.exitc <- err

				if err != nil {
					if time.Now().Sub(t.wstatus.started) < time.Second {
						time.Sleep(restartWait)
					}
				} else {
					t.want = down
				}
			case callback := <-t.downc:
				t.want = down
				t.signal(syscall.SIGTERM)
				<-exitc
				callback <- true
			}
		}
	}
}

func (t *task) transition(w string) error {
	cb := make(chan bool, 1)

	switch w {
	case up:
		t.upc <- cb
	case down:
		t.downc <- cb
	}

	select {
	case ok := <-cb:
		if ok {
			break
		} else {
			return errors.New(t.status())
		}
	case <-time.After(timeout):
		return errors.New("timeout: " + t.status())
	}
	return nil
}
