package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"

	"github.com/Sirupsen/logrus"
	common "github.com/iotbzh/xds-common/golib"
	"github.com/iotbzh/xds-server/lib/apiv1"
	"github.com/iotbzh/xds-server/lib/crosssdk"
	"github.com/iotbzh/xds-server/lib/xdsconfig"
	sio_client "github.com/zhouhui8915/go-socket.io-client"
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

	httpCli *common.HTTPClient
	ioSock  *sio_client.Client

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

	// Define HTTP and WS url
	baseURL := g.uri
	if !strings.HasPrefix(g.uri, "http://") {
		baseURL = "http://" + g.uri
	}

	// Create HTTP client
	g.log.Infoln("Connect HTTP client on ", baseURL)
	conf := common.HTTPClientConfig{
		URLPrefix:           "/api/v1",
		HeaderClientKeyName: "XDS-SID",
		CsrfDisable:         true,
	}
	c, err := common.HTTPNewClient(baseURL, conf)
	if err != nil {
		return int(syscallEBADE), err
	}
	g.httpCli = c

	// First call to check that xds-server is alive
	var data []byte
	if err := c.HTTPGet("/folders", &data); err != nil {
		return int(syscallEBADE), err
	}
	g.log.Infof("Result of /folders: %v", string(data[:]))

	// Check mandatory args
	if g.prjID == "" || g.listPrj {
		return getProjectsList(c, g.log, data)
	}

	// Create io Websocket client
	g.log.Infoln("Connecting IO.socket client on ", baseURL)

	opts := &sio_client.Options{
		Transport: "websocket",
		Header:    make(map[string][]string),
	}
	opts.Header["XDS-SID"] = []string{c.GetClientID()}

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

	iosk.On(apiv1.ExecOutEvent, func(ev apiv1.ExecOutMsg) {
		if g.cbRead != nil {
			g.cbRead(ev.Timestamp, ev.Stdout, ev.Stderr)
		}
	})

	iosk.On(apiv1.ExecInferiorOutEvent, func(ev apiv1.ExecOutMsg) {
		if g.cbInferiorRead != nil {
			g.cbInferiorRead(ev.Timestamp, ev.Stdout, ev.Stderr)
		}
	})

	iosk.On(apiv1.ExecExitEvent, func(ev apiv1.ExecExitMsg) {
		if g.cbOnExit != nil {
			g.cbOnExit(ev.Code, ev.Error)
		}
	})

	return 0, nil
}

func (g *GdbXds) Close() error {
	g.cbOnDisconnect = nil
	g.cbOnError = nil
	g.cbOnExit = nil
	g.cbRead = nil
	g.cbInferiorRead = nil

	return nil
}

// Start sends a request to start remotely gdb within xds-server
func (g *GdbXds) Start(inferiorTTY bool) (int, error) {
	var body []byte
	var err error

	// Enable workaround about inferior output with gdbserver connection
	// except if XDS_GDBSERVER_OUTPUT_NOFIX is defined
	_, gdbserverNoFix := os.LookupEnv("XDS_GDBSERVER_OUTPUT_NOFIX")

	args := apiv1.ExecArgs{
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
	body, err = json.Marshal(args)
	if err != nil {
		return int(syscallEBADE), err
	}

	g.log.Infof("POST %s/exec %v", g.uri, string(body))
	var res *http.Response
	var found bool
	res, err = g.httpCli.HTTPPostWithRes("/exec", string(body))
	if err != nil {
		return int(syscall.EAGAIN), err
	}
	dRes := make(map[string]interface{})
	json.Unmarshal(g.httpCli.ResponseToBArray(res), &dRes)
	if _, found = dRes["cmdID"]; !found {
		return int(syscallEBADE), err
	}
	g.cmdID = dRes["cmdID"].(string)

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
	return g.ioSock.Emit(apiv1.ExecInEvent, args...)
}

// SendSignal is used to send a signal to remote process/gdb
func (g *GdbXds) SendSignal(sig os.Signal) error {
	var body []byte
	body, err := json.Marshal(apiv1.ExecSignalArgs{
		CmdID:  g.cmdID,
		Signal: sig.String(),
	})
	if err != nil {
		g.log.Errorf(err.Error())
	}
	g.log.Debugf("POST /signal %s", string(body))
	return g.httpCli.HTTPPost("/signal", string(body))
}

//***** Private functions *****

func getProjectsList(c *common.HTTPClient, log *logrus.Logger, prjData []byte) (int, error) {

	folders := xdsconfig.FoldersConfig{}
	errMar := json.Unmarshal(prjData, &folders)
	msg := ""
	if errMar == nil {
		msg += "List of existing projects (use: export XDS_PROJECT_ID=<< ID >>): \n"
		msg += "  ID\t\t\t\t | Label"
		for _, f := range folders {
			msg += fmt.Sprintf("\n  %s\t | %s", f.ID, f.Label)
			if f.DefaultSdk != "" {
				msg += fmt.Sprintf("\t(default SDK: %s)", f.DefaultSdk)
			}
		}
		msg += "\n"
	}

	var data []byte
	if err := c.HTTPGet("/sdks", &data); err != nil {
		return int(syscallEBADE), err
	}
	log.Infof("Result of /sdks: %v", string(data[:]))

	sdks := []crosssdk.SDK{}
	errMar = json.Unmarshal(data, &sdks)
	if errMar == nil {
		msg += "\nList of installed cross SDKs (use: export XDS_SDK_ID=<< ID >>): \n"
		msg += "  ID\t\t\t\t\t | NAME\n"
		for _, s := range sdks {
			msg += fmt.Sprintf("  %s\t | %s\n", s.ID, s.Name)
		}
	}

	if len(folders) > 0 && len(sdks) > 0 {
		msg += fmt.Sprintf("\n")
		msg += fmt.Sprintf("For example: \n")
		msg += fmt.Sprintf("  XDS_PROJECT_ID=%q XDS_SDK_ID=%q  %s -x myGdbConf.ini\n",
			folders[0].ID, sdks[0].ID, AppName)
	}

	return 0, fmt.Errorf(msg)
}