// xds-gdb: a wrapper on gdb tool for X(cross) Development System.
package main

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"os/user"
	"syscall"
	"time"

	"strings"

	"path"

	"github.com/Sirupsen/logrus"
	"github.com/codegangsta/cli"
	common "github.com/iotbzh/xds-common/golib"
	"github.com/joho/godotenv"
)

var appAuthors = []cli.Author{
	cli.Author{Name: "Sebastien Douheret", Email: "sebastien@iot.bzh"},
}

// AppName name of this application
var AppName = "xds-gdb"

// AppVersion Version of this application
// (set by Makefile)
var AppVersion = "?.?.?"

// AppSubVersion is the git tag id added to version string
// Should be set by compilation -ldflags "-X main.AppSubVersion=xxx"
// (set by Makefile)
var AppSubVersion = "unknown-dev"

// Create logger
var log = logrus.New()
var logFileInitial = "/tmp/xds-gdb.log"

// Application details
const (
	appCopyright    = "Apache-2.0"
	defaultLogLevel = "warning"
)

// Exit events
type exitResult struct {
	error error
	code  int
}

// EnvVar - Environment variables used by application
type EnvVar struct {
	Name        string
	Usage       string
	Destination *string
}

// exitError terminates this program with the specified error
func exitError(code syscall.Errno, f string, a ...interface{}) {
	err := fmt.Sprintf(f, a...)
	fmt.Fprintf(os.Stderr, err+"\n")
	log.Debugf("Exit: code=%v, err=%s", code, err)

	os.Exit(int(code))
}

// main
func main() {
	var uri, prjID, rPath, logLevel, logFile, sdkid, confFile, gdbNative string
	var listProject bool
	var err error

	// Init Logger and set temporary file and level for the 1st part
	// IOW while XDS_LOGLEVEL and XDS_LOGFILE options are not parsed
	logFile = logFileInitial
	fdL, err := os.OpenFile(logFileInitial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		msgErr := fmt.Sprintf("Cannot create log file %s", logFileInitial)
		exitError(syscall.EPERM, msgErr)
	}
	log.Formatter = &logrus.TextFormatter{}
	log.Out = fdL
	log.Level = logrus.DebugLevel

	uri = "localhost:8000"
	logLevel = defaultLogLevel

	// Create a new App instance
	app := cli.NewApp()
	app.Name = AppName
	app.Usage = "wrapper on gdb for X(cross) Development System."
	app.Version = AppVersion + " (" + AppSubVersion + ")"
	app.Authors = appAuthors
	app.Copyright = appCopyright
	app.Metadata = make(map[string]interface{})
	app.Metadata["version"] = AppVersion
	app.Metadata["git-tag"] = AppSubVersion
	app.Metadata["logger"] = log

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:        "list, ls",
			Usage:       "list existing xds projects",
			Destination: &listProject,
		},
	}

	appEnvVars := []EnvVar{
		EnvVar{
			Name:        "XDS_CONFIG",
			Usage:       "env config file to source on startup",
			Destination: &confFile,
		},
		EnvVar{
			Name:        "XDS_LOGLEVEL",
			Usage:       "logging level (supported levels: panic, fatal, error, warn, info, debug)",
			Destination: &logLevel,
		},
		EnvVar{
			Name:        "XDS_LOGFILE",
			Usage:       "logging file",
			Destination: &logFile,
		},
		EnvVar{
			Name:        "XDS_NATIVE_GDB",
			Usage:       "use native gdb instead of remote XDS server",
			Destination: &gdbNative,
		},
		EnvVar{
			Name:        "XDS_PROJECT_ID",
			Usage:       "project ID you want to build (mandatory variable)",
			Destination: &prjID,
		},
		EnvVar{
			Name:        "XDS_RPATH",
			Usage:       "relative path into project",
			Destination: &rPath,
		},
		EnvVar{
			Name:        "XDS_SDK_ID",
			Usage:       "Cross Sdk ID to use to build project",
			Destination: &sdkid,
		},
		EnvVar{
			Name:        "XDS_SERVER_URL",
			Usage:       "remote XDS server url",
			Destination: &uri,
		},
	}

	// Process gdb arguments
	log.Debugf("xds-gdb started with args: %v", os.Args)
	args := make([]string, len(os.Args))
	args[0] = os.Args[0]
	gdbArgs := make([]string, len(os.Args))

	// Split xds-xxx options from gdb options
	copy(gdbArgs, os.Args[1:])
	for idx, a := range os.Args[1:] {
		// Specific case to print help or version of xds-gdb
		switch a {
		case "--help", "-h", "--version", "-v", "--list", "-ls":
			args[1] = a
			goto endloop
		case "--":
			// Detect skip option (IOW '--') to split arguments
			copy(args, os.Args[0:idx+1])
			copy(gdbArgs, os.Args[idx+2:])
			goto endloop
		}
	}
endloop:

	// Parse gdb arguments to detect:
	//  --tty option: used for inferior/ tty of debugged program
	//  -x/--command option: XDS env vars may be set within gdb command file
	clientPty := ""
	gdbCmdFile := ""
	for idx, a := range gdbArgs {
		switch {
		case strings.HasPrefix(a, "--tty="):
			clientPty = a[len("--tty="):]
			gdbArgs[idx] = ""

		case a == "--tty":
		case strings.HasPrefix(a, "-tty"):
			clientPty = gdbArgs[idx+1]
			gdbArgs[idx] = ""
			gdbArgs[idx+1] = ""

		case strings.HasPrefix(a, "--command="):
			gdbCmdFile = a[len("--command="):]

		case a == "--command":
		case strings.HasPrefix(a, "-x"):
			gdbCmdFile = gdbArgs[idx+1]
		}
	}

	// Source config env file
	// (we cannot use confFile var because env variables setting is just after)
	envMap, confFile, err := loadConfigEnvFile(os.Getenv("XDS_CONFIG"), gdbCmdFile)
	log.Infof("Load env config: envMap=%v, confFile=%v, err=%v", envMap, confFile, err)

	// Only rise an error when args is not set (IOW when --help or --version is not set)
	if len(args) == 1 {
		if err != nil {
			exitError(syscall.ENOENT, err.Error())
		}
	}

	// Managed env vars and create help
	dynDesc := "\nENVIRONMENT VARIABLES:"
	for _, ev := range appEnvVars {
		dynDesc += fmt.Sprintf("\n %s \t\t %s", ev.Name, ev.Usage)
		if evVal, evExist := os.LookupEnv(ev.Name); evExist && ev.Destination != nil {
			*ev.Destination = evVal
		}
	}
	app.Description = "gdb wrapper for X(cross) Development System\n"
	app.Description += "\n"
	app.Description += " Two debugging models are supported:\n"
	app.Description += "  - xds remote debugging requiring an XDS server and allowing cross debug\n"
	app.Description += "  - native debugging\n"
	app.Description += " By default xds remote debug is used and you need to define XDS_NATIVE_GDB to\n"
	app.Description += " use native gdb debug mode instead.\n"
	app.Description += "\n"
	app.Description += " xds-gdb configuration (see variables list below) can be set using:\n"
	app.Description += "  - a config file (XDS_CONFIG)\n"
	app.Description += "  - or environment variables\n"
	app.Description += "  - or by setting variables within gdb ini file (commented line including :XDS-ENV: tag)\n"
	app.Description += "    Example of gdb ini file where we define project and sdk ID:\n"
	app.Description += "     # :XDS-ENV: XDS_PROJECT_ID=IW7B4EE-DBY4Z74_myProject\n"
	app.Description += "     # :XDS-ENV: XDS_SDK_ID=poky-agl_aarch64_3.99.1+snapshot\n"
	app.Description += "\n"
	app.Description += dynDesc + "\n"

	// only one action
	app.Action = func(ctx *cli.Context) error {
		var err error
		curDir, _ := os.Getwd()

		// Build env variables
		env := []string{}
		for k, v := range envMap {
			env = append(env, k+"="+v)
		}

		// Now set logger level and log file to correct/env var settings
		if log.Level, err = logrus.ParseLevel(logLevel); err != nil {
			msg := fmt.Sprintf("Invalid log level : \"%v\"\n", logLevel)
			return cli.NewExitError(msg, int(syscall.EINVAL))
		}
		log.Infof("Switch log level to %s", logLevel)

		if logFile != logFileInitial {
			log.Infof("Switch logging to log file %s", logFile)

			fdL, err := os.OpenFile(logFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
			if err != nil {
				msgErr := fmt.Sprintf("Cannot create log file %s", logFile)
				return cli.NewExitError(msgErr, int(syscall.EPERM))
			}
			defer fdL.Close()
			log.Out = fdL
		}

		// Create cross or native gdb interface
		var gdb IGDB
		if gdbNative != "" {
			gdb = NewGdbNative(log, gdbArgs, env)
		} else {
			gdb = NewGdbXds(log, gdbArgs, env)
			gdb.SetConfig("uri", uri)
			gdb.SetConfig("prjID", prjID)
			gdb.SetConfig("sdkID", sdkid)
			gdb.SetConfig("rPath", rPath)
			gdb.SetConfig("listProject", listProject)
		}

		// Log useful info
		log.Infof("Original arguments: %v", os.Args)
		log.Infof("Current directory : %v", curDir)
		log.Infof("Use confFile      : '%s'", confFile)
		log.Infof("Execute           : /exec %v %v", gdb.Cmd(), gdb.Args())

		// Properly report invalid init file error
		gdbCommandFileError := ""
		for i, a := range gdbArgs {
			if a == "-x" {
				gdbCommandFileError = gdbArgs[i+1] + ": No such file or directory."
				break
			} else if strings.HasPrefix(a, "--command=") {
				gdbCommandFileError = strings.TrimLeft(a, "--command=") + ": No such file or directory."
				break
			}
		}
		log.Infof("Add detection of error: <%s>", gdbCommandFileError)

		// Init gdb subprocess management
		if code, err := gdb.Init(); err != nil {
			return cli.NewExitError(err.Error(), code)
		}

		exitChan := make(chan exitResult, 1)

		gdb.OnError(func(err error) {
			fmt.Println("ERROR: ", err.Error())
		})

		gdb.OnDisconnect(func(err error) {
			fmt.Println("Disconnection: ", err.Error())
			exitChan <- exitResult{err, int(syscall.ESHUTDOWN)}
		})

		gdb.Read(func(timestamp, stdout, stderr string) {
			if stdout != "" {
				fmt.Printf("%s", stdout)
				log.Debugf("Recv OUT: <%s>", stdout)
			}
			if stderr != "" {
				fmt.Fprintf(os.Stderr, "%s", stderr)
				log.Debugf("Recv ERR: <%s>", stderr)
			}

			// Correctly report error about init file
			if gdbCommandFileError != "" && strings.Contains(stdout, gdbCommandFileError) {
				fmt.Fprintf(os.Stderr, "ERROR: "+gdbCommandFileError)
				log.Errorf("ERROR: " + gdbCommandFileError)
				if err := gdb.SendSignal(syscall.SIGTERM); err != nil {
					log.Errorf("Error while sending signal: %s", err.Error())
				}
				exitChan <- exitResult{err, int(syscall.ENOENT)}
			}
		})

		gdb.OnExit(func(code int, err error) {
			exitChan <- exitResult{err, code}
		})

		// Handle client tty / pts
		if clientPty != "" {
			log.Infoln("Client tty detected: %v\n", clientPty)

			cpFd, err := os.OpenFile(clientPty, os.O_RDWR, 0)
			if err != nil {
				return cli.NewExitError(err.Error(), int(syscall.EPERM))
			}
			defer cpFd.Close()

			// client tty stdin
			/* XXX TODO - implement stdin to send data to debugged program
			go func() {
				reader := bufio.NewReader(cpFd)
				sc := bufio.NewScanner(reader)
				for sc.Scan() {
					data := sc.Text()
					iosk.Emit(apiv1.ExecInferiorInEvent, data+"\n")
					log.Debugf("Inferior IN: <%v>", data)
				}
				if sc.Err() != nil {
					log.Warnf("Inferior Stdin scanner exit, close stdin (err=%v)", sc.Err())
				}
			}()
			*/

			// client tty stdout
			gdb.InferiorRead(func(timestamp, stdout, stderr string) {
				if stdout != "" {
					fmt.Fprintf(cpFd, "%s", stdout)
					log.Debugf("Inferior OUT: <%s>", stdout)
				}
				if stderr != "" {
					fmt.Fprintf(cpFd, "%s", stderr)
					log.Debugf("Inferior ERR: <%s>", stderr)
				}
			})
		}

		// Allow to overwrite some gdb commands
		var overwriteMap = make(map[string]string)
		if overEnv, exist := os.LookupEnv("XDS_OVERWRITE_COMMANDS"); exist {
			overEnvS := strings.TrimSpace(overEnv)
			if len(overEnvS) > 0 {
				// Extract overwrite commands from env variable
				for _, def := range strings.Split(overEnvS, ",") {
					if kv := strings.Split(def, ":"); len(kv) == 2 {
						overwriteMap[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
					} else {
						return cli.NewExitError(
							fmt.Errorf("Invalid definition in XDS_OVERWRITE_COMMANDS (%s)", def),
							int(syscall.EINVAL))
					}
				}
			}
		} else {
			overwriteMap["-exec-run"] = "-exec-continue"
			overwriteMap["-file-exec-and-symbols"] = "-file-exec-file"
		}
		log.Debugf("overwriteMap = %v", overwriteMap)

		// Send stdin though WS
		go func() {
			paranoia := 600
			reader := bufio.NewReader(os.Stdin)

			for {
				sc := bufio.NewScanner(reader)
				for sc.Scan() {
					command := sc.Text()

					// overwrite some commands
					for key, value := range overwriteMap {
						if strings.Contains(command, key) {
							command = strings.Replace(command, key, value, 1)
							log.Debugf("OVERWRITE %s -> %s", key, value)
						}
					}
					gdb.Write(command + "\n")
					log.Debugf("Send: <%v>", command)
				}
				log.Infof("Stdin scanner exit, close stdin (err=%v)", sc.Err())

				// CTRL-D exited scanner, so send it explicitly
				gdb.Write("\x04")
				time.Sleep(time.Millisecond * 100)

				if paranoia--; paranoia <= 0 {
					msg := "Abnormal loop detected on stdin"
					log.Errorf("Abnormal loop detected on stdin")
					gdb.SendSignal(syscall.SIGTERM)
					exitChan <- exitResult{fmt.Errorf(msg), int(syscall.ELOOP)}
				}
			}
		}()

		// Handling all Signals
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs)

		go func() {
			for {
				sig := <-sigs
				if err := gdb.SendSignal(sig); err != nil {
					log.Errorf("Error while sending signal: %s", err.Error())
				}
			}
		}()

		// Start gdb
		if code, err := gdb.Start(clientPty != ""); err != nil {
			return cli.NewExitError(err.Error(), code)
		}

		// Wait exit
		select {
		case res := <-exitChan:
			errStr := ""
			if res.code == 0 {
				log.Infoln("Exit successfully")
			}
			if res.error != nil {
				log.Infoln("Exit with ERROR: ", res.error.Error())
				errStr = res.error.Error()
			}
			return cli.NewExitError(errStr, res.code)
		}
	}

	app.Run(args)
}

// loadConfigEnvFile
func loadConfigEnvFile(confFile, gdbCmdFile string) (map[string]string, string, error) {
	var err error
	envMap := make(map[string]string)

	// 1- if no confFile set, use setting from gdb command file is option
	//    --command/-x is set
	if confFile == "" && gdbCmdFile != "" {
		log.Infof("Try extract config from gdbCmdFile: %s", gdbCmdFile)
		confFile, err = extractEnvFromCmdFile(gdbCmdFile)
		if confFile != "" {
			defer os.Remove(confFile)
		}
		if err != nil {
			log.Infof("Extraction from gdbCmdFile failed: %v", err.Error())
		}
	}
	// 2- search xds-gdb.env file in various locations
	if confFile == "" {
		curDir, _ := os.Getwd()
		if u, err := user.Current(); err == nil {
			xdsEnvFile := "xds-gdb.env"
			for _, d := range []string{
				path.Join(curDir),
				path.Join(curDir, ".."),
				path.Join(curDir, "target"),
				path.Join(u.HomeDir, ".config", "xds"),
			} {
				confFile = path.Join(d, xdsEnvFile)
				log.Infof("Search config in %s", confFile)
				if common.Exists(confFile) {
					break
				}
			}
		}
	}

	if confFile == "" {
		log.Infof("NO valid conf file found!")
		return envMap, "", nil
	}

	if !common.Exists(confFile) {
		return envMap, confFile, fmt.Errorf("Error no env config file not found")
	}
	if err = godotenv.Load(confFile); err != nil {
		return envMap, confFile, fmt.Errorf("Error loading env config file " + confFile)
	}
	if envMap, err = godotenv.Read(confFile); err != nil {
		return envMap, confFile, fmt.Errorf("Error reading env config file " + confFile)
	}

	return envMap, confFile, nil
}

/*
 extractEnvFromCmdFile: extract xds-gdb env variable from gdb command file
  All commented lines (#) in gdb command file that start with ':XDS-ENV:' prefix
  will be considered as XDS env commands. For example the 3 syntaxes below
  are supported:
  # :XDS-ENV: XDS_PROJECT_ID=IW7B4EE-DBY4Z74_myProject
  #:XDS-ENV:XDS_SDK_ID=poky-agl_aarch64_3.99.1+snapshot
  # :XDS-ENV:  export XDS_SERVER_URL=localhost:8800
*/
func extractEnvFromCmdFile(cmdFile string) (string, error) {
	if !common.Exists(cmdFile) {
		return "", nil
	}
	cFd, err := os.Open(cmdFile)
	if err != nil {
		return "", fmt.Errorf("Cannot open %s : %s", cmdFile, err.Error())
	}
	defer cFd.Close()

	var lines []string
	scanner := bufio.NewScanner(cFd)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err = scanner.Err(); err != nil {
		return "", fmt.Errorf("Cannot parse %s : %s", cmdFile, err.Error())
	}

	envFile, err := ioutil.TempFile("", "xds-gdb_env.ini")
	if err != nil {
		return "", fmt.Errorf("Error while creating temporary env file: %s", err.Error())
	}
	envFileName := envFile.Name()
	defer envFile.Close()

	envFound := false
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "#") && strings.Contains(ln, ":XDS-ENV:") {
			env := strings.SplitAfterN(ln, ":XDS-ENV:", 2)
			if len(env) == 2 {
				envFound = true
				if _, err := envFile.WriteString(strings.TrimSpace(env[1]) + "\n"); err != nil {
					return "", fmt.Errorf("Error write into temporary env file: %s", err.Error())
				}
			} else {
				log.Warnf("Error while decoding line %s", ln)
			}
		}
	}

	if !envFound {
		ff := envFileName
		defer os.Remove(ff)
		envFileName = ""

	}

	return envFileName, nil
}
