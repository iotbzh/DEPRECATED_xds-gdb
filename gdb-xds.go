/*
 * Copyright (C) 2017 "IoT.bzh"
 * Author Sebastien Douheret <sebastien@iot.bzh>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/Sirupsen/logrus"
	"github.com/iotbzh/xds-agent/lib/xaapiv1"
	common "github.com/iotbzh/xds-common/golib"
	sio_client "github.com/sebd71/go-socket.io-client"
)

// GdbXds - Implementation of IGDB used to interfacing XDS
type GdbXds struct {
	log       *logrus.Logger
	ccmd      string
	aargs     []string
	eenv      []string
	agentURL  string
	serverURL string
	prjID     string
	sdkID     string
	rPath     string
	listPrj   bool
	cmdID     string
	xGdbPid   string

	httpCli *common.HTTPClient
	ioSock  *sio_client.Client

	projects []xaapiv1.ProjectConfig

	// callbacks
	cbOnError      func(error)
	cbOnDisconnect func(error)
	cbRead         func(timestamp, stdout, stderr string)
	cbInferiorRead func(timestamp, stdout, stderr string)
	cbOnExit       func(code int, err error)
}

// NewGdbXds creates a new instance of GdbXds
func NewGdbXds(log *logrus.Logger, args []string, env []string) *GdbXds {
	return &GdbXds{
		log:     log,
		ccmd:    "exec $GDB", // var set by environment-setup-xxx script
		aargs:   args,
		eenv:    env,
		httpCli: nil,
		ioSock:  nil,
		xGdbPid: strconv.Itoa(os.Getpid()),
	}
}

// SetConfig set additional config fields
func (g *GdbXds) SetConfig(name string, value interface{}) error {
	switch name {
	case "agentURL":
		g.agentURL = value.(string)
	case "serverURL":
		g.serverURL = value.(string)
	case "prjID":
		g.prjID = value.(string)
	case "sdkID":
		g.sdkID = value.(string)
	case "rPath":
		g.rPath = value.(string)
	case "listProject":
		g.listPrj = value.(bool)
	default:
		return fmt.Errorf("Unknown %s field", name)
	}
	return nil
}

// Init initializes gdb XDS
func (g *GdbXds) Init() (int, error) {

	// Reset command ID (also used to enable sending of signals)
	g.cmdID = ""

	// Define HTTP and WS url
	baseURL := g.agentURL

	// Allow to only set port number
	if match, _ := regexp.MatchString("^([0-9]+)$", baseURL); match {
		baseURL = "http://localhost:" + g.agentURL
	}
	// Add http prefix if missing
	if baseURL != "" && !strings.HasPrefix(g.agentURL, "http://") {
		baseURL = "http://" + g.agentURL
	}

	// Create HTTP client
	g.log.Infoln("Connect HTTP client on ", baseURL)
	conf := common.HTTPClientConfig{
		URLPrefix:           "/api/v1",
		HeaderClientKeyName: "Xds-Agent-Sid",
		CsrfDisable:         true,
		LogOut:              g.log.Out,
		LogPrefix:           "XDSAGENT: ",
		LogLevel:            common.HTTPLogLevelDebug,
	}
	c, err := common.HTTPNewClient(baseURL, conf)
	if err != nil {
		errmsg := err.Error()
		m, err := regexp.MatchString("Get http.?://", errmsg)
		if (m && err == nil) || strings.Contains(errmsg, "Failed to get device ID") {
			i := strings.LastIndex(errmsg, ":")
			newErr := "Cannot connection to " + baseURL
			if i > 0 {
				newErr += " (" + strings.TrimSpace(errmsg[i+1:]) + ")"
			} else {
				newErr += " (" + strings.TrimSpace(errmsg) + ")"
			}
			errmsg = newErr
		}
		return int(syscallEBADE), fmt.Errorf(errmsg)
	}
	g.httpCli = c
	g.httpCli.SetLogLevel(g.log.Level.String())
	g.log.Infoln("HTTP session ID:", g.httpCli.GetClientID())

	// First call to check that xds-agent and server are alive
	ver := xaapiv1.XDSVersion{}
	if err := g.httpCli.Get("/version", &ver); err != nil {
		return int(syscallEBADE), err
	}
	g.log.Infoln("XDS agent & server version:", ver)

	// Get current config and update connection to server when needed
	xdsConf := xaapiv1.APIConfig{}
	if err := g.httpCli.Get("/config", &xdsConf); err != nil {
		return int(syscallEBADE), err
	}
	// FIXME: add multi-servers support
	idx := 0
	svrCfg := xdsConf.Servers[idx]
	if g.serverURL != "" && (svrCfg.URL != g.serverURL || !svrCfg.Connected) {
		svrCfg.URL = g.serverURL
		svrCfg.ConnRetry = 10
		newCfg := xaapiv1.APIConfig{}
		if err := g.httpCli.Post("/config", xdsConf, &newCfg); err != nil {
			return int(syscallEBADE), err
		}

	} else if !svrCfg.Connected {
		return int(syscallEBADE), fmt.Errorf("XDS server not connected (url=%s)", svrCfg.URL)
	}

	// Get XDS projects list
	var data []byte
	if err := g.httpCli.HTTPGet("/projects", &data); err != nil {
		return int(syscallEBADE), err
	}

	g.log.Infof("Result of /projects: %v", string(data[:]))
	g.projects = []xaapiv1.ProjectConfig{}
	errMar := json.Unmarshal(data, &g.projects)
	if errMar != nil {
		g.log.Errorf("Cannot decode projects configuration: %s", errMar.Error())
	}

	// Check mandatory args
	if g.prjID == "" || g.listPrj {
		return g.printProjectsList()
	}

	// Create io Websocket client
	g.log.Infoln("Connecting IO.socket client on ", baseURL)

	opts := &sio_client.Options{
		Transport: "websocket",
		Header:    make(map[string][]string),
	}
	opts.Header["XDS-AGENT-SID"] = []string{c.GetClientID()}

	iosk, err := sio_client.NewClient(baseURL, opts)
	if err != nil {
		e := fmt.Sprintf("IO.socket connection error: " + err.Error())
		return int(syscall.ECONNABORTED), fmt.Errorf(e)
	}
	g.ioSock = iosk

	iosk.On("error", func(err error) {
		if g.cbOnError != nil {
			g.cbOnError(err)
		}
	})

	iosk.On("disconnection", func(err error) {
		if g.cbOnDisconnect != nil {
			g.cbOnDisconnect(err)
		}
	})

	// SEB gdbPid := ""
	iosk.On(xaapiv1.ExecOutEvent, func(ev xaapiv1.ExecOutMsg) {
		if g.cbRead != nil {
			g.cbRead(ev.Timestamp, ev.Stdout, ev.Stderr)
			/*
				stdout := ev.Stdout
				// SEB
				//New Thread 15139
				if strings.Contains(stdout, "pid = ") {
					re := regexp.MustCompile("pid = ([0-9]+)")
					if res := re.FindAllStringSubmatch(stdout, -1); len(res) > 0 {
						gdbPid = res[0][1]
					}
					g.log.Errorf("SEB FOUND THREAD in '%s' => gdbPid=%s", stdout, gdbPid)
				}
				if gdbPid != "" && g.xGdbPid != "" && strings.Contains(stdout, gdbPid) {
					g.log.Errorf("SEB THREAD REPLACE 1 stdout=%s", stdout)
					stdout = strings.Replace(stdout, gdbPid, g.xGdbPid, -1)
					g.log.Errorf("SEB THREAD REPLACE 2 stdout=%s", stdout)
				}

				g.cbRead(ev.Timestamp, stdout, ev.Stderr)
			*/
		}
	})

	iosk.On(xaapiv1.ExecInferiorOutEvent, func(ev xaapiv1.ExecOutMsg) {
		if g.cbInferiorRead != nil {
			g.cbInferiorRead(ev.Timestamp, ev.Stdout, ev.Stderr)
		}
	})

	iosk.On(xaapiv1.ExecExitEvent, func(ev xaapiv1.ExecExitMsg) {
		if g.cbOnExit != nil {
			g.cbOnExit(ev.Code, ev.Error)
		}
	})

	return 0, nil
}

// Close frees allocated objects and close opened connections
func (g *GdbXds) Close() error {
	g.cbOnDisconnect = nil
	g.cbOnError = nil
	g.cbOnExit = nil
	g.cbRead = nil
	g.cbInferiorRead = nil
	g.cmdID = ""

	return nil
}

// Start sends a request to start remotely gdb within xds-server
func (g *GdbXds) Start(inferiorTTY bool) (int, error) {
	var err error
	var project *xaapiv1.ProjectConfig

	// Retrieve the project definition
	for _, f := range g.projects {
		// check as prefix to support short/partial id name
		if strings.HasPrefix(f.ID, g.prjID) {
			project = &f
			break
		}
	}

	// Auto setup rPath if needed
	if g.rPath == "" && project != nil {
		cwd, err := os.Getwd()
		if err == nil {
			fldRp := project.ClientPath
			if !strings.HasPrefix(fldRp, "/") {
				fldRp = "/" + fldRp
			}
			log.Debugf("Try to auto-setup rPath: cwd=%s ; ClientPath=%s", cwd, fldRp)
			if sp := strings.SplitAfter(cwd, fldRp); len(sp) == 2 {
				g.rPath = strings.Trim(sp[1], "/")
				g.log.Debugf("Auto-setup rPath to: '%s'", g.rPath)
			}
		}
	}

	// Enable workaround about inferior output with gdbserver connection
	// except if XDS_GDBSERVER_OUTPUT_NOFIX is defined
	_, gdbserverNoFix := os.LookupEnv("XDS_GDBSERVER_OUTPUT_NOFIX")

	args := xaapiv1.ExecArgs{
		ID:              g.prjID,
		SdkID:           g.sdkID,
		Cmd:             g.ccmd,
		Args:            g.aargs,
		Env:             g.eenv,
		RPath:           g.rPath,
		TTY:             inferiorTTY,
		TTYGdbserverFix: !gdbserverNoFix,
		CmdTimeout:      -1, // no timeout, end when stdin close or command exited normally
	}

	g.log.Infof("POST %s/exec %v", g.agentURL, args)
	res := xaapiv1.ExecResult{}
	err = g.httpCli.Post("/exec", args, &res)
	if err != nil {
		return int(syscall.EAGAIN), err
	}
	if res.CmdID == "" {
		return int(syscallEBADE), fmt.Errorf("null CmdID")
	}
	g.cmdID = res.CmdID

	return 0, nil
}

// Cmd returns the command name
func (g *GdbXds) Cmd() string {
	return g.ccmd
}

// Args returns the list of arguments
func (g *GdbXds) Args() []string {
	return g.aargs
}

// Env returns the list of environment variables
func (g *GdbXds) Env() []string {
	return g.eenv
}

// OnError is called on a WebSocket error
func (g *GdbXds) OnError(f func(error)) {
	g.cbOnError = f
}

// OnDisconnect is called when WebSocket disconnection
func (g *GdbXds) OnDisconnect(f func(error)) {
	g.cbOnDisconnect = f
}

// OnExit calls when exit event is received
func (g *GdbXds) OnExit(f func(code int, err error)) {
	g.cbOnExit = f
}

// Read calls when a message/string event is received on stdout or stderr
func (g *GdbXds) Read(f func(timestamp, stdout, stderr string)) {
	g.cbRead = f
}

// InferiorRead calls when a message/string event is received on stdout or stderr of the debugged program (IOW inferior)
func (g *GdbXds) InferiorRead(f func(timestamp, stdout, stderr string)) {
	g.cbInferiorRead = f
}

// Write writes message/string into gdb stdin
func (g *GdbXds) Write(args ...interface{}) error {
	return g.ioSock.Emit(xaapiv1.ExecInEvent, args...)
}

// SendSignal is used to send a signal to remote process/gdb
func (g *GdbXds) SendSignal(sig os.Signal) error {
	if g.cmdID == "" {
		return fmt.Errorf("cmdID not set")
	}

	sigArg := xaapiv1.ExecSignalArgs{
		CmdID:  g.cmdID,
		Signal: sig.String(),
	}
	g.log.Debugf("POST /signal %v", sigArg)
	return g.httpCli.Post("/signal", sigArg, nil)
}

//***** Private functions *****

func (g *GdbXds) printProjectsList() (int, error) {
	writer := new(tabwriter.Writer)
	writer.Init(os.Stdout, 0, 8, 0, '\t', 0)
	msg := ""
	if len(g.projects) > 0 {
		fmt.Fprintln(writer, "List of existing projects (use: export XDS_PROJECT_ID=<< ID >>):")
		fmt.Fprintln(writer, "ID \t Label")
		for _, f := range g.projects {
			fmt.Fprintf(writer, " %s \t  %s\n", f.ID, f.Label)
		}
	}

	// FIXME : support multiple servers
	sdks := []xaapiv1.SDK{}
	if err := g.httpCli.Get("/servers/0/sdks", &sdks); err != nil {
		return int(syscallEBADE), err
	}
	fmt.Fprintln(writer, "\nList of installed cross SDKs (use: export XDS_SDK_ID=<< ID >>):")
	fmt.Fprintln(writer, "ID \t Name")
	for _, s := range sdks {
		fmt.Fprintf(writer, " %s \t  %s\n", s.ID, s.Name)
	}

	if len(g.projects) > 0 && len(sdks) > 0 {
		fmt.Fprintln(writer, "")
		fmt.Fprintln(writer, "For example: ")
		fmt.Fprintf(writer, "  XDS_PROJECT_ID=%s XDS_SDK_ID=%s  %s -x myGdbConf.ini\n",
			g.projects[0].ID[:8], sdks[0].ID[:8], AppName)
	}
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "Or define settings within gdb configuration file (see help and :XDS-ENV: tag)")
	writer.Flush()

	return 0, fmt.Errorf(msg)
}
