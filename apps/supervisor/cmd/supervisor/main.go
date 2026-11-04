// Command supervisor runs the gateway and domain services as loopback processes
// inside one Cloudflare Container, which has no private network between Containers.
// It exits when any process does, so the edge controller restarts the Container.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const drainTimeout = 45 * time.Second

var domains = []struct {
	name string
	port int
}{
	{"identity", 8081},
	{"hiring", 8082},
	{"documents", 8083},
	{"evaluation", 8084},
	{"billing", 8085},
}

type process struct {
	name string
	cmd  *exec.Cmd
	done chan error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	peers := peerURLs()
	var processes []*process
	exited := make(chan string, len(domains)+1)
	start := func(name string, environment []string) {
		child, err := launch(name, environment, exited)
		if err != nil {
			stopAll(processes)
			log.Fatalf("start %s: %v", name, err)
		}
		processes = append(processes, child)
	}
	for _, domain := range domains {
		start(domain.name, domainEnvironment(os.Environ(), domain.name, domain.port, peers))
	}
	start("gateway", gatewayEnvironment(os.Environ(), peers))

	failed := false
	select {
	case <-ctx.Done():
	case name := <-exited:
		log.Printf("%s exited; stopping the Container", name)
		failed = true
	}
	stopAll(processes)
	if failed {
		os.Exit(1)
	}
}

func launch(name string, environment []string, exited chan<- string) (*process, error) {
	command := exec.Command("/app/" + name)
	command.Env, command.Stdout, command.Stderr = environment, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	child := &process{name: name, cmd: command, done: make(chan error, 1)}
	go func() {
		child.done <- command.Wait()
		close(child.done)
		exited <- name
	}()
	return child, nil
}

func stopAll(processes []*process) {
	for _, child := range processes {
		_ = child.cmd.Process.Signal(syscall.SIGTERM)
	}
	deadline := time.After(drainTimeout)
	for _, child := range processes {
		select {
		case <-child.done:
		case <-deadline:
			log.Printf("%s did not drain in time", child.name)
			_ = child.cmd.Process.Kill()
			<-child.done
		}
	}
}

func peerURLs() []string {
	urls := make([]string, 0, len(domains))
	for _, domain := range domains {
		urls = append(urls, fmt.Sprintf("%s_URL=http://127.0.0.1:%d", strings.ToUpper(domain.name), domain.port))
	}
	return urls
}

// domainEnvironment confines payment credentials to the billing process.
func domainEnvironment(base []string, name string, port int, peers []string) []string {
	environment := make([]string, 0, len(base)+len(peers)+1)
	for _, entry := range base {
		if name != "billing" && strings.HasPrefix(entry, "RAZORPAY_") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, peers...)
	return append(environment, fmt.Sprintf("PORT=%d", port))
}

// gatewayEnvironment gives the public entry point no database or application secrets.
func gatewayEnvironment(base, peers []string) []string {
	environment := make([]string, 0, len(peers)+2)
	for _, entry := range base {
		if strings.HasPrefix(entry, "WEB_URL=") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, peers...)
	return append(environment, "PORT=8000")
}
