package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"github.com/jessevdk/go-flags"
)

const HelperInitProgName = "__cgrun_init__"

var mandatoryParameters = map[string][]string{
	"cpuset": []string{
		"cpus",
		"mems",
	},
}

var subsysMountPoints = make(map[string]string)

func initMountPointMap() error {
	// First, read available cgroup subsystems
	entries, err := ioutil.ReadFile("/proc/cgroups")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(entries), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 1 {
			continue
		}

		subsysMountPoints[f[0]] = ""
	}

	entries, err = ioutil.ReadFile("/proc/mounts")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(entries), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}

		if f[2] != "cgroup" {
			continue
		}
		for _, opt := range strings.Split(f[3], ",") {
			if _, ok := subsysMountPoints[opt]; ok {
				subsysMountPoints[opt] = f[1] // path
			}
		}
	}

	return nil
}

func makeHierarchyName() string {
	// This might be unique at the moment
	seed := time.Now().Unix() + int64(os.Getpid())
	hash := md5.New()
	fmt.Fprintf(hash, "%d", seed)
	return hex.EncodeToString(hash.Sum(nil))
}

var childStarted = false

func setupSignalHandler(handler func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	go func() {
		<-sigCh
		if !childStarted {
			handler()
		}
	}()
}

func setupHierarchy(hirName string, params map[string]map[string]string) (err error) {
	// Now we have to ensure that the cleanup will be done even in case of signaled
	setupSignalHandler(func() {
		cleanupHierarchy(hirName, params)
	})
	defer func() {
		if err != nil {
			cleanupHierarchy(hirName, params)
		}
	}()

	for subsys, values := range params {
		mountPoint, ok := subsysMountPoints[subsys]
		if !ok || mountPoint == "" {
			return fmt.Errorf("subsystem '%s' is not mounted", subsys)
		}

		hirPath := filepath.Join(mountPoint, hirName)
		if err := os.Mkdir(hirPath, 0750); err != nil {
			return err
		}
		if mandParams, ok := mandatoryParameters[subsys]; ok {
			// Copy mandatory parameters from parent hierarchy
			for _, param := range mandParams {
				parentPath := filepath.Join(filepath.Dir(hirPath), subsys+"."+param)
				buf, err := ioutil.ReadFile(parentPath)
				if err != nil {
					return err
				}

				path := filepath.Join(hirPath, subsys+"."+param)
				if err := ioutil.WriteFile(path, buf, 0); err != nil {
					return err
				}
			}
		}

		for param, val := range values {
			path := filepath.Join(hirPath, subsys+"."+param)
			if err := ioutil.WriteFile(path, []byte(val), 0); err != nil {
				return err
			}
		}
	}

	return nil
}

func cleanupHierarchy(hirName string, params map[string]map[string]string) {
	for subsys, _ := range params {
		mountPoint, ok := subsysMountPoints[subsys]
		if !ok || mountPoint == "" {
			continue
		}

		hirPath := filepath.Join(mountPoint, hirName)
		// This should not be RemoveAll since the cgroup is a special file system
		// and does understand the mean of 'rmdir' operation for it's subdirectory.
		if err := os.Remove(hirPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "failed to cleanup '%s': %s\n", hirPath, err)
		}
	}
}

func getTasksFiles(hirName string, params map[string]map[string]string) ([]string, error) {
	var helperArgs []string
	for subsys, _ := range params {
		mountPoint, ok := subsysMountPoints[subsys]
		if !ok || mountPoint == "" {
			return nil, fmt.Errorf("subsystem '%s' is not mounted", subsys)
		}
		helperArgs = append(helperArgs, filepath.Join(mountPoint, hirName, "tasks"))
	}
	return helperArgs, nil
}

func execProgram(hirName string, params map[string]map[string]string, args []string) (int, error) {
	helperArgs, err := getTasksFiles(hirName, params)
	if err != nil {
		return -1, err
	}
	helperArgs = append(helperArgs, "--")
	helperArgs = append(helperArgs, args...)

	selfPath, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return -1, err
	}
	cmd := exec.Command(selfPath, helperArgs...)
	cmd.Args[0] = HelperInitProgName
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return -1, err
	}
	// Below just ignore a signal.
	// Child will be exit by propagated signal and we'll gonna exit properly.
	childStarted = true
	fmt.Fprintln(os.Stderr, hirName)

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				return status.ExitStatus(), nil
			}
		}
		return -1, err
	}
	return 0, nil
}

func isPidFile(name string) bool {
	for _, c := range name {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func collectPids(pid string, tasksFiles []string) error {
	pidByte := []byte(pid)
	for _, tasksFile := range tasksFiles {
		if err := ioutil.WriteFile(tasksFile, pidByte, 0); err != nil {
			return err
		}
	}

	if !opts.Tree {
		return nil
	}

	// Search for my children
	dp, err := os.Open("/proc")
	if err != nil {
		return err
	}
	dirEnts, err := dp.Readdirnames(-1)
	dp.Close()
	if err != nil {
		return err
	}
	for _, name := range dirEnts {
		if !isPidFile(name) {
			continue
		}
		buf, err := ioutil.ReadFile("/proc/"+name+"/stat")
		if err != nil {
			return err
		}
		f := strings.Fields(string(buf))
		if f[3] == pid {
			if err := collectPids(f[0], tasksFiles); err != nil {
				return err
			}
		}
	}

	return nil
}

// TODO probably this can be done better by using memory.oom_control
func waitNonChildPid(pid int) {
	for syscall.Kill(pid, 0) == nil {
		time.Sleep(500 * time.Millisecond)
	}
}

func seizePid(hirName string, params map[string]map[string]string, pid int) error {
	childStarted = true
	tasksFiles, err := getTasksFiles(hirName, params)
	if err != nil {
		return err
	}
	if err := collectPids(fmt.Sprintf("%d", pid), tasksFiles); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, hirName)
	waitNonChildPid(pid)
	return nil
}

func initialMain() int {
	args, err := flags.ParseArgs(&opts, os.Args[1:])
	if err != nil {
		if err.(*flags.Error).Type == flags.ErrHelp {
			return 0
		} else {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	baseParent := opts.Parent
	for len(baseParent) > 0 && baseParent[0] == '/' {
		baseParent = baseParent[1:]
	}

	params := make(map[string]map[string]string)
	for i, arg := range args {
		sep := strings.Index(arg, "=")
		if sep == -1 {
			if arg == "--" {
				i++
			}
			args = args[i:]
			break
		}
		// cpu.shares=1024 -> cpu.shares(param), 1024(value)
		param := arg[:sep]
		value := arg[sep+1:]
		sep = strings.Index(param, ".")
		if sep == -1 {
			fmt.Fprintf(os.Stderr, "incorrect parameter name: '%s'\n", param)
			return 1
		}
		// cpu.shares -> cpu(subsys), shares
		subsys := param[:sep]
		if _, ok := params[subsys]; !ok {
			params[subsys] = make(map[string]string)
		}
		params[subsys][param[sep+1:]] = value
	}

	if err := initMountPointMap(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build cgroup fs mount point map: %s\n", err)
		return 1
	}

	hirName := baseParent + makeHierarchyName()
	if err := setupHierarchy(hirName, params); err != nil {
		fmt.Fprintf(os.Stderr, "failed to setup cgroup hierarchy: %s\n", err)
		return 1
	}
	defer cleanupHierarchy(hirName, params)

	if opts.Pid != nil {
		if *opts.Pid <= 0 {
			fmt.Fprintf(os.Stderr, "invalid pid %d\n", *opts.Pid)
			return 1
		}
		if err := seizePid(hirName, params, *opts.Pid); err != nil {
			fmt.Fprintf(os.Stderr, "can't attach to process %d: %s\n", *opts.Pid, err)
			return 1
		}
		return 0
	} else {
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "no target program specified\n")
			return 1
		}
		exitStatus, err := execProgram(hirName, params, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to execute command: %s\n", err)
			return 1
		}
		return exitStatus
	}
}

func helperMain() {
	args := os.Args[1:]

	pid := []byte(fmt.Sprintf("%d", os.Getpid()))
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
		if err := ioutil.WriteFile(arg, pid, 0); err != nil {
			fmt.Fprintf(os.Stderr, "can't write pid to %s: %s\n", arg, err)
			return
		}
	}

	binPath, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to lookup path of '%s': %s\n", args[0], err)
		return
	}

	if err := syscall.Exec(binPath, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "can't exec '%s': %s\n", args[0], err)
	}
}

var opts struct {
	Parent string `short:"P" long:"parent" value-name:"PARENT" default:"/" description:"Parent hierarchy that should be inherited"`

	// For attach mode
	Pid *int `short:"p" long:"pid" value-name:"PID" description:"The target pid to attach volatile cgroup"`
	Tree bool `short:"T" long:"tree" description:"When used with -p option, decide whether attach for whole process tree or not"`
}

func main() {
	if os.Args[0] == HelperInitProgName {
		helperMain()
		os.Exit(1) // Never returns on success
	}
	os.Exit(initialMain())
}
