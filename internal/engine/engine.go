// Package engine defines the runtime boundary used by the agent. HTTP, files,
// process supervision, and operation/session state remain outside the driver.
package engine

import (
	"context"
	"time"
)

type Command struct {
	Program string
	Args    []string
	Dir     string
	Env     []string
}

// Run captures bounded stdout and stderr separately and supervises cancellation.
type Run func(context.Context, Command) (stdout, stderr []byte, err error)

type Stack struct {
	Name, Dir, ComposeFile, EnvFile string
}

type SelfContainer struct {
	ID, Image, Project string
	Mounts             []ContainerMount
}

type ContainerMount struct {
	Type, Source, Destination string
	RW                        bool
}

type DetachedState struct {
	Running  bool
	ExitCode int
	Output   string
}

// DetachedOperations optionally runs own-stack actions outside the node container.
type DetachedOperations interface {
	SelfContainer(context.Context) (*SelfContainer, error)
	StartDetached(context.Context, SelfContainer, string, string, Stack) error
	DetachedState(context.Context, string) (DetachedState, error)
	RemoveDetached(context.Context, string) error
}

type Error struct {
	Code int
	// Message is safe to expose in API responses. Cause holds internal diagnostics.
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if nil != e.Cause {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

type Service struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Service       string        `json:"service"`
	Image         string        `json:"image"`
	State         string        `json:"state"`
	Health        string        `json:"health"`
	ExitCode      *int          `json:"exit_code,omitempty"`
	Status        string        `json:"status,omitempty"`
	CreatedAt     string        `json:"created_at,omitempty"`
	StartedAt     string        `json:"started_at,omitempty"`
	RunningFor    string        `json:"running_for,omitempty"`
	Ports         string        `json:"ports"`
	PortBindings  []PortBinding `json:"port_bindings"`
	ImageUpdate   *ImageUpdate  `json:"image_update,omitempty"`
	CheckInterval string        `json:"check_interval,omitempty"`
	AutoUpdate    bool          `json:"auto_update,omitempty"`
}

type PortBinding struct {
	HostIP        string `json:"host_ip"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

type ImageUpdate struct {
	Status    string     `json:"status"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

type ImagePlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
	OSVersion    string `json:"os.version"`
}

type ImageKey struct {
	Ref string
	ImagePlatform
}

type ImageTarget struct {
	LocalID string
	Key     ImageKey
	// Status/reason are set for unsupported images or failed inspection.
	Status, Reason string
}

type RegistryImage struct {
	Digest, Reason, Status string
	Cooldown               *RegistryCooldown
	CheckedAt              time.Time
}

type RegistryCooldown struct {
	Registry string    `json:"registry"`
	Account  string    `json:"account"`
	Until    time.Time `json:"until"`
	Attempts int       `json:"attempts"`
}

type NodeInfo struct {
	Runtime    RuntimeInfo      `json:"runtime"`
	Containers []ContainerStats `json:"containers"`
	CheckedAt  time.Time        `json:"checked_at"`
}

type RuntimeInfo struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	OperatingSystem   string `json:"operating_system"`
	Architecture      string `json:"architecture"`
	KernelVersion     string `json:"kernel_version"`
	CPUs              int    `json:"cpus"`
	MemoryBytes       int64  `json:"memory_bytes"`
	Containers        int    `json:"containers"`
	ContainersRunning int    `json:"containers_running"`
	ContainersStopped int    `json:"containers_stopped"`
	ContainersPaused  int    `json:"containers_paused"`
	Images            int    `json:"images"`
	StorageDriver     string `json:"storage_driver"`
}

type ContainerStats struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CPU           string `json:"cpu"`
	Memory        string `json:"memory"`
	MemoryPercent string `json:"memory_percent"`
	NetworkIO     string `json:"network_io"`
	BlockIO       string `json:"block_io"`
	PIDs          string `json:"pids"`
}

type ImageSource interface {
	InspectImage(context.Context, Service) ImageTarget
	ImageRef(string) string
}

// Driver covers only engine-specific commands and their wire formats. Commands
// are executed by the agent so streamed output and PTYs share one lifecycle.
type Driver interface {
	ImageSource
	Check(context.Context) error
	NodeInfo(context.Context) (NodeInfo, error)
	Validate(context.Context, Stack) error
	Containers(context.Context, Stack) ([]Service, error)
	Stats(context.Context, Stack) ([]ContainerStats, error)
	Services(context.Context, Stack) ([]string, error)
	ImageRefs(context.Context, Stack) (map[string]bool, error)
	ImageChanged(context.Context, Service) (bool, error)
	Action(Stack, string, string) Command
	Logs(Stack) Command
	Probe(context.Context) error
	Events(time.Time) Command
	EventStack([]byte) string
	ResolveContainer(context.Context, Stack, string) (string, error)
	Shell(string, string, string) Command
	CleanupShell(context.Context, string, string) error
}
