package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/iotbzh/xds-agent/lib/xaapiv1"
	common "github.com/iotbzh/xds-common/golib"
	sio_client "github.com/sebd71/go-socket.io-client"
)

// GdbXds -
type GdbXds struct {
	log     *logrus.Logger
	ccmd    string
	aargs   []string
	eenv    []string
	uri     string
	prjID   string
	sdkID   string
	rPath   string
	listPrj bool
	cmdID   string
	xGdbPid string

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
	case "uri":
		g.uri = value.(string)
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
	baseURL := g.uri
	if !strings.HasPrefix(g.uri, "http://") {
		baseURL = "http://" + g.uri
	}

	// Create HTTP client
	g.log.Infoln("Connect HTTP client on ", baseURL)
	conf := common.HTTPClientConfig{
		URLPrefix:           "/api/v1",
		HeaderClientKeyName: "Xds-Agent-Sid",
		CsrfDisable:         true,
		LogOut:              g.log.Out,
		LogLevel:            common.HTTPLogLevelWarning,
	}
	c, err := common.HTTPNewClient(baseURL, conf)
	if err != nil {
		errmsg := err.Error()
		if m, err := regexp.MatchString("Get http.?://", errmsg); m && err == nil {
			i := strings.LastIndex(errmsg, ":")
			errmsg = "Cannot connection to " + baseURL + errmsg[i:]
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

	// SEB Check that server is connected
	// FIXME: add multi-servers support

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

	iosk.On(xaapiv1.ExecOutEvent, func(ev xaapiv1.ExecOutMsg) {
		if g.cbRead != nil {
			g.cbRead(ev.Timestamp, ev.Stdout, ev.Stderr)
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

	g.log.Infof("POST %s/exec %v", g.uri, args)
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
	msg := ""
	if len(g.projects) > 0 {
		msg += "List of existing projects (use: export XDS_PROJECT_ID=<< ID >>): \n"
		msg += "  ID\t\t\t\t | Label"
		for _, f := range g.projects {
			msg += fmt.Sprintf("\n  %s\t | %s", f.ID, f.Label)
			if f.DefaultSdk != "" {
				msg += fmt.Sprintf("\t(default SDK: %s)", f.DefaultSdk)
			}
		}
		msg += "\n"
	}

	// FIXME : support multiple servers
	sdks := []xaapiv1.SDK{}
	if err := g.httpCli.Get("/servers/0/sdks", &sdks); err != nil {
		return int(syscallEBADE), err
	}
	msg += "\nList of installed cross SDKs (use: export XDS_SDK_ID=<< ID >>): \n"
	msg += "  ID\t\t\t\t\t | NAME\n"
	for _, s := range sdks {
		msg += fmt.Sprintf("  %s\t | %s\n", s.ID, s.Name)
	}

	if len(g.projects) > 0 && len(sdks) > 0 {
		msg += fmt.Sprintf("\n")
		msg += fmt.Sprintf("For example: \n")
		msg += fmt.Sprintf("  XDS_PROJECT_ID=%q XDS_SDK_ID=%q  %s -x myGdbConf.ini\n",
			g.projects[0].ID, sdks[0].ID, AppName)
	}

	return 0, fmt.Errorf(msg)
}
