package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func TestBackupFiles(t *testing.T) {
	a, dir := fixture(t)
	first := seed(t, a, "first", "compose.yaml")
	write(t, filepath.Join(first, ".env"), "PASSWORD=secret\r\n")
	write(t, filepath.Join(first, "unrelated.txt"), "not a stack file")
	second := seed(t, a, "second", "docker-compose.yml")
	write(t, filepath.Join(second, ".env"), "")
	seed(t, a, "third", "compose.yml")
	w := request(a, "GET", "/backup", "")
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	var backup protocol.NodeBackup
	if err := json.Unmarshal(w.Body.Bytes(), &backup); nil != err {
		t.Fatal(err)
	}
	files := make(map[string]string)
	for _, file := range backup.Files {
		files[file.Path] = string(file.Data)
	}
	compose, err := os.ReadFile(filepath.Join(first, "compose.yaml"))
	if nil != err {
		t.Fatal(err)
	}
	want := map[string]string{
		"first/compose.yaml":        string(compose),
		"first/.env":                "PASSWORD=secret\r\n",
		"second/docker-compose.yml": string(compose),
		"second/.env":               "",
		"third/compose.yml":         string(compose),
	}
	if !reflect.DeepEqual(want, files) {
		t.Fatalf("backup files: %v", files)
	}
	if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
		t.Fatal("backup invoked runtime tools")
	}
}

func TestBackupReadFailure(t *testing.T) {
	a, _ := fixture(t)
	dir := seed(t, a, "broken", "compose.yaml")
	if err := os.Mkdir(filepath.Join(dir, ".env"), 0700); nil != err {
		t.Fatal(err)
	}
	w := request(a, "GET", "/backup", "")
	var backup protocol.NodeBackup
	if err := json.Unmarshal(w.Body.Bytes(), &backup); nil != err || 400 != w.Code || "" == backup.Error || 0 != len(backup.Files) || strings.Contains(w.Body.String(), dir) {
		t.Fatal(w.Code, w.Body.String())
	}
}
