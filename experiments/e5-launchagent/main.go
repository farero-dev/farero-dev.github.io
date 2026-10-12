// e5-launchagent is the M0 probe for running farerod as a plain user
// LaunchAgent (~/Library/LaunchAgents + launchctl) instead of SMAppService.
// SMAppService ties the background item to an ad-hoc binary's code hash, so
// launchd refuses a replaced farerod (Launch Constraint Violation). The probe
// appends "<version> <pid> <unix time>" to -log every second so a test can
// see which build launchd is running.
//
//	go build -ldflags "-X main.version=v1" -o probe .
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

var version = "dev"

func main() {
	out := flag.String("log", "", "heartbeat log path")
	flag.Parse()
	for {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%s %d %d\n", version, os.Getpid(), time.Now().Unix())
			f.Close()
		}
		time.Sleep(time.Second)
	}
}
