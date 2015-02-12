// +build linux

package libcontainer

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"

	"github.com/golang/glog"

	cgroups "github.com/docker/libcontainer/cgroups/manager"
	"github.com/docker/libcontainer/configs"
	"github.com/docker/libcontainer/configs/validate"
)

const (
	stateFilename = "state.json"
)

var (
	idRegex  = regexp.MustCompile(`^[\w_]+$`)
	maxIdLen = 1024
)

// New returns a linux based container factory based in the root directory.
func New(root string, initArgs []string) (Factory, error) {
	if root != "" {
		if err := os.MkdirAll(root, 0700); err != nil {
			return nil, newGenericError(err, SystemError)
		}
	}
	return &linuxFactory{
		root:      root,
		initArgs:  initArgs,
		validator: validate.New(),
	}, nil
}

// linuxFactory implements the default factory interface for linux based systems.
type linuxFactory struct {
	// root is the root directory
	root      string
	initArgs  []string
	validator validate.Validator
}

func (l *linuxFactory) Create(id string, config *configs.Config) (Container, error) {
	if l.root == "" {
		return nil, newGenericError(fmt.Errorf("invalid root"), ConfigInvalid)
	}
	if err := l.validateID(id); err != nil {
		return nil, err
	}
	if err := l.validator.Validate(config); err != nil {
		return nil, newGenericError(err, ConfigInvalid)
	}
	containerRoot := filepath.Join(l.root, id)
	if _, err := os.Stat(containerRoot); err == nil {
		return nil, newGenericError(fmt.Errorf("Container with id exists: %v", id), IdInUse)
	} else if !os.IsNotExist(err) {
		return nil, newGenericError(err, SystemError)
	}
	if err := os.MkdirAll(containerRoot, 0700); err != nil {
		return nil, newGenericError(err, SystemError)
	}
	return &linuxContainer{
		id:            id,
		root:          containerRoot,
		config:        config,
		initArgs:      l.initArgs,
		cgroupManager: cgroups.NewCgroupManager(config.Cgroups),
	}, nil
}

func (l *linuxFactory) Load(id string) (Container, error) {
	if l.root == "" {
		return nil, newGenericError(fmt.Errorf("invalid root"), ConfigInvalid)
	}
	containerRoot := filepath.Join(l.root, id)
	state, err := l.loadState(containerRoot)
	if err != nil {
		return nil, err
	}
	r := &restoredProcess{
		processPid:       state.InitProcessPid,
		processStartTime: state.InitProcessStartTime,
	}
	cgroupManager := cgroups.LoadCgroupManager(state.Config.Cgroups, state.CgroupPaths)
	glog.Infof("using %s as cgroup manager", cgroupManager)
	return &linuxContainer{
		initProcess:   r,
		id:            id,
		config:        &state.Config,
		initArgs:      l.initArgs,
		cgroupManager: cgroupManager,
		root:          containerRoot,
	}, nil
}

// StartInitialization loads a container by opening the pipe fd from the parent to read the configuration and state
// This is a low level implementation detail of the reexec and should not be consumed externally
func (l *linuxFactory) StartInitialization(pipefd uintptr) (err error) {
	var (
		pipe = os.NewFile(uintptr(pipefd), "pipe")
		it   = initType(os.Getenv("_LIBCONTAINER_INITTYPE"))
	)
	// clear the current process's environment to clean any libcontainer
	// specific env vars.
	os.Clearenv()
	defer func() {
		// if we have an error during the initialization of the container's init then send it back to the
		// parent process in the form of an initError.
		if err != nil {
			// ensure that any data sent from the parent is consumed so it doesn't
			// receive ECONNRESET when the child writes to the pipe.
			ioutil.ReadAll(pipe)
			if err := json.NewEncoder(pipe).Encode(initError{
				Message: err.Error(),
			}); err != nil {
				panic(err)
			}
		}
		// ensure that this pipe is always closed
		pipe.Close()
	}()
	i, err := newContainerInit(it, pipe)
	if err != nil {
		return err
	}
	return i.Init()
}

func (l *linuxFactory) loadState(root string) (*State, error) {
	f, err := os.Open(filepath.Join(root, stateFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, newGenericError(err, ContainerNotExists)
		}
		return nil, newGenericError(err, SystemError)
	}
	defer f.Close()
	var state *State
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return nil, newGenericError(err, SystemError)
	}
	return state, nil
}

func (l *linuxFactory) validateID(id string) error {
	if !idRegex.MatchString(id) {
		return newGenericError(fmt.Errorf("Invalid id format: %v", id), InvalidIdFormat)
	}
	if len(id) > maxIdLen {
		return newGenericError(fmt.Errorf("Invalid id format: %v", id), InvalidIdFormat)
	}
	return nil
}

// restoredProcess represents a process where the calling process may or may not be
// the parent process.  This process is created when a factory loads a container from
// a persisted state.
type restoredProcess struct {
	processPid       int
	processStartTime string
}

func (p *restoredProcess) start() error {
	return newGenericError(fmt.Errorf("restored process cannot be started"), SystemError)
}

func (p *restoredProcess) pid() int {
	return p.processPid
}

func (p *restoredProcess) terminate() error {
	return newGenericError(fmt.Errorf("restored process cannot be terminated"), SystemError)
}

func (p *restoredProcess) wait() (*os.ProcessState, error) {
	return nil, newGenericError(fmt.Errorf("restored process cannot be waited on"), SystemError)
}

func (p *restoredProcess) startTime() (string, error) {
	return p.processStartTime, nil
}

func (p *restoredProcess) signal(s os.Signal) error {
	return newGenericError(fmt.Errorf("restored process cannot be signaled"), SystemError)
}
