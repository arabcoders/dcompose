package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func executionFixture(t *testing.T) (*agent, string) {
	t.Helper()
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(fake, "ps"), `[{"ID":"one","Name":"web-1","Service":"web","State":"running"}]`)
	return a, fake
}

func TestServiceUpdateStopped(t *testing.T) {
	a, fake := executionFixture(t)
	write(t, filepath.Join(fake, "ps"), `[{"Name":"web-1","Service":"web","State":"exited"},{"Name":"worker-1","Service":"worker","State":"running"}]`)
	op := completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"update","service":"web"}`))
	argv, err := os.ReadFile(filepath.Join(fake, "argv"))
	if nil != err || "succeeded" != op.Status || strings.Contains(string(argv), "<pull>") || strings.Contains(string(argv), "<up>") {
		t.Fatalf("stopped service update: %s %v", argv, err)
	}
}

func TestHelperRegistryService(t *testing.T) {
	a, fake := executionFixture(t)
	registryFixture(t, a)
	applyRegistryFixture(t, a)
	restarted := newAgent(a.ctx, a.config)
	if err := restarted.initRegistries(filepath.Dir(a.registries.root)); nil != err {
		t.Fatal(err)
	}
	a = restarted
	write(t, filepath.Join(fake, "ps"), `[{"Name":"web-1","Service":"web","State":"running"},{"Name":"worker-1","Service":"worker","State":"running"}]`)
	dir, err := a.stackPath("demo", false)
	if nil != err {
		t.Fatal(err)
	}
	stack, err := a.engineStack("demo", dir)
	if nil != err {
		t.Fatal(err)
	}
	a.operationsDir = t.TempDir()
	op := &operation{ID: "0123456789abcdef", Stack: "demo", Action: "update", Status: "running"}
	if err := a.prepareDetached(op, stack, "web"); nil != err {
		t.Fatal(err)
	}
	requestFile := filepath.Join(a.operationsDir, op.ID, "request.json")
	if err := RunOperation(a.ctx, requestFile); nil != err {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(filepath.Join(fake, "argv"))
	if nil != err || !strings.Contains(string(argv), "<pull><--><web>") || !strings.Contains(string(argv), "<up><-d><--no-deps><--><web>") || strings.Contains(string(argv), "secret") {
		t.Fatalf("helper arguments: %s %v", argv, err)
	}
	env, err := os.ReadFile(filepath.Join(fake, "pull-config"))
	if nil != err || a.pullConfigDir() != string(env) {
		t.Fatalf("helper credential environment: %q %v", env, err)
	}
	if readDockerConfig(t, filepath.Join(a.pullConfigDir(), "config.json")).Auths["https://index.docker.io/v1/"]["auth"] != loginAuth("pull", "pull-secret")["auth"] {
		t.Fatal("helper lost the shared pull login across restart")
	}
	data, err := os.ReadFile(requestFile)
	if nil != err || strings.Contains(string(data), "secret") || strings.Contains(string(data), "username") {
		t.Fatal("helper request contains credentials")
	}
}
