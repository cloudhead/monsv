package main

import (
	"bufio"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Commands
const (
	up     = "up"
	down   = "down"
	status = "status"
	exit   = "exit"
)

var Version string

var (
	cmds        = []string{up, down, status, exit}
	restartWait = time.Second
	timeout     = 7 * time.Second
	errStatus   = 111
)

func listenAndNotify(addr string, notif chan string) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()

	for {
		conn, err := l.Accept()
		if err != nil {
			continue
		}

		go func(conn net.Conn) {
			defer conn.Close()

			for {
				str, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				notif <- strings.TrimSpace(str)
				conn.Write([]byte(<-notif + "\n"))
			}
		}(conn)
	}
}

func control(ch chan string, exitc chan os.Signal, inTask, outTask *task) {
	for cmd := range ch {
		switch cmd {
		case up, down:
			if inTask.want == cmd {
				ch <- inTask.status()
				break
			}
			if err := inTask.transition(cmd); err != nil {
				ch <- err.Error()
			} else {
				ch <- inTask.status()
			}
		case status:
			ch <- inTask.status() + "\n" + outTask.status()
		case exit:
			exitc <- os.Kill
			return
		default:
			ch <- strings.Join(cmds, ", ")
		}
	}
}

func exits(inTask, outTask *task, alarm bool) {
	for {
		select {
		case <-inTask.exitc:
			if alarm {
				ok, err := outTask.signal(syscall.SIGALRM)
				if ok && err != nil {
					log.Printf("error sending exit signal to logger (pid %d): %s", outTask.cmd.Process.Pid, err)
				} else if !ok {
					log.Printf("error sending exit signal to logger: process not running")
				}
			}
		case <-outTask.exitc:
			// Ignore
		}
	}
}

func main() {
	var (
		laddr   string
		incmd   string
		outcmd  string
		inargs  args
		outargs args
		alarm   bool
	)

	flag.StringVar(&laddr, "laddr", "", "listen address")
	flag.StringVar(&incmd, "sv.cmd", "tee", "service command to run")
	flag.StringVar(&outcmd, "log.cmd", "tee", "logger command to run")
	flag.BoolVar(&alarm, "log.alarm", false, "send SIGALRM to logger when service exits")
	flag.Var(&inargs, "sv.arg", "service command argument (may be specified multiple times)")
	flag.Var(&outargs, "log.arg", "logger command argument (may be specified multiple times)")

	flag.Parse()

	log.SetPrefix(`[monsv] `)

	ctrl := make(chan string)

	if laddr != "" {
		go listenAndNotify(laddr, ctrl)
	}

	inTask := NewTask(incmd, inargs...)
	outTask := NewTask(outcmd, outargs...)
	outTask.isLog = true

	read, write, err := os.Pipe()
	if err != nil {
		log.Println(err)
		os.Exit(errStatus)
	}
	defer read.Close()
	defer write.Close()

	go inTask.run(os.Stdin, write, write)
	go outTask.run(read, os.Stdout, os.Stderr)

	for _, t := range []*task{outTask, inTask} {
		if err := t.transition(up); err != nil {
			log.Fatal(err)
		}
	}
	exitc := make(chan os.Signal, 1)

	go control(ctrl, exitc, inTask, outTask)
	go exits(inTask, outTask, alarm)

	signal.Notify(exitc, syscall.SIGTERM, syscall.SIGINT)

	<-exitc

	for _, t := range []*task{inTask, outTask} {
		if err := t.transition(down); err != nil {
			t.signal(syscall.SIGKILL)
		}
	}
}
