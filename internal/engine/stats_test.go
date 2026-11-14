package engine_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/docker"
	"github.com/arabcoders/dcompose/internal/engine/podman"
)

func statsDriver(name string, run engine.Run) engine.Driver {
	if "podman" == name {
		return podman.New(run)
	}
	return docker.New(run)
}

func TestStackStats(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			sample := 0
			calls := 0
			driver := statsDriver(name, func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				switch cmd.Args[0] {
				case "compose":
					want := []string{
						"compose",
						"--project-name",
						"demo",
						"--project-directory",
						"/stacks/demo",
						"--env-file",
						"/dev/null",
						"--file",
						"/stacks/demo/compose.yaml",
						"ps",
						"--all",
						"--format",
						"json",
					}
					if !reflect.DeepEqual(want, cmd.Args) {
						t.Fatalf("discovery arguments: %v", cmd.Args)
					}
					sample++
					if 1 == sample {
						return []byte(
							`[{"ID":"web-id","Name":"demo-web-1","State":"running"},{"Name":"demo-worker-1","State":"running"},{"ID":"stopped-id","Name":"demo-old-1","State":"exited"}]`,
						), nil, nil
					}
					return []byte(`[{"ID":"new-id","Name":"demo-web-1","State":"running"}]`), nil, nil
				case "container":
					return []byte(`{}`), nil, nil
				case "stats":
					calls++
					format := "{{json .}}"
					if "podman" == name {
						format = "json"
					}
					want := []string{"stats", "--no-stream", "--no-trunc", "--format", format, "web-id", "demo-worker-1"}
					if 2 == sample {
						want = []string{"stats", "--no-stream", "--no-trunc", "--format", format, "new-id"}
					}
					if name != cmd.Program || !reflect.DeepEqual(want, cmd.Args) {
						t.Fatalf("scoped stats command: %+v", cmd)
					}
					if "podman" == name {
						return []byte(
							`[{"id":"sample-id","name":"demo-web-1","cpu_percent":"2.5%","mem_usage":"1MiB / 8MiB","mem_percent":"12.5%","net_io":"1kB / 2kB","block_io":"3kB / 4kB","pids":"5"}]`,
						), nil, nil
					}
					return []byte(
						`{"ID":"sample-id","Name":"demo-web-1","CPUPerc":"2.5%","MemUsage":"1MiB / 8MiB","MemPerc":"12.5%","NetIO":"1kB / 2kB","BlockIO":"3kB / 4kB","PIDs":"5"}`,
					), nil, nil
				default:
					t.Fatalf("unexpected command: %+v", cmd)
					return nil, nil, nil
				}
			})
			stack := engine.Stack{Name: "demo", Dir: "/stacks/demo", ComposeFile: "/stacks/demo/compose.yaml", EnvFile: "/dev/null"}
			want := engine.ContainerStats{
				ID:            "sample-id",
				Name:          "demo-web-1",
				CPU:           "2.5%",
				Memory:        "1MiB / 8MiB",
				MemoryPercent: "12.5%",
				NetworkIO:     "1kB / 2kB",
				BlockIO:       "3kB / 4kB",
				PIDs:          "5",
			}
			for range 2 {
				stats, err := driver.Stats(context.Background(), stack)
				if nil != err || 1 != len(stats) || want != stats[0] {
					t.Fatalf("stats: %+v, %v", stats, err)
				}
			}
			if 2 != sample || 2 != calls {
				t.Fatalf("discovery/stats calls: %d/%d", sample, calls)
			}
		})
	}
}

func TestStackStatsEmpty(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		for _, containers := range []string{`[]`, `[{"ID":"stopped","Name":"demo-web-1","State":"exited"}]`} {
			t.Run(name+containers, func(t *testing.T) {
				driver := statsDriver(name, func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
					if "compose" == cmd.Args[0] {
						return []byte(containers), nil, nil
					}
					if "container" == cmd.Args[0] {
						return []byte(`{}`), nil, nil
					}
					t.Fatalf("empty stack ran stats: %+v", cmd)
					return nil, nil, nil
				})
				stats, err := driver.Stats(context.Background(), engine.Stack{Name: "demo"})
				if nil != err || nil == stats || 0 != len(stats) {
					t.Fatalf("empty stack stats: %+v, %v", stats, err)
				}
			})
		}
	}
}

func TestStackStatsErrors(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		for _, failure := range []string{"compose", "stats", "decode"} {
			t.Run(name+failure, func(t *testing.T) {
				driver := statsDriver(name, func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
					if failure == cmd.Args[0] {
						return []byte("stdout-secret"), []byte("stderr-secret"), context.DeadlineExceeded
					}
					if "compose" == cmd.Args[0] {
						return []byte(`[{"ID":"id","Name":"demo-web-1","State":"running"}]`), nil, nil
					}
					if "container" == cmd.Args[0] {
						return []byte(`{}`), nil, nil
					}
					return []byte("stdout-secret"), nil, nil
				})
				_, err := driver.Stats(context.Background(), engine.Stack{Name: "demo"})
				apiErr, ok := errors.AsType[*engine.Error](err)
				if !ok || 502 != apiErr.Code || strings.Contains(apiErr.Message, "secret") {
					t.Fatalf("unsafe stats error: %v", err)
				}
				if "decode" != failure && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost cancellation cause: %v", err)
				}
			})
		}
	}
}

func TestStackStatsCancel(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		for _, stage := range []string{"compose", "stats"} {
			t.Run(name+stage, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				driver := statsDriver(name, func(ctx context.Context, cmd engine.Command) ([]byte, []byte, error) {
					if stage == cmd.Args[0] {
						cancel()
						<-ctx.Done()
						return nil, nil, ctx.Err()
					}
					if "compose" == cmd.Args[0] {
						return []byte(`[{"ID":"id","Name":"demo-web-1","State":"running"}]`), nil, nil
					}
					return []byte(`{}`), nil, nil
				})
				_, err := driver.Stats(ctx, engine.Stack{Name: "demo"})
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
			})
		}
	}
}
