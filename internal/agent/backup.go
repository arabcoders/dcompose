package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func (a *agent) backup(ctx context.Context, w http.ResponseWriter) error {
	entries, err := os.ReadDir(a.config.Global.StacksDir)
	if nil != err {
		return err
	}
	backup := protocol.NodeBackup{Files: []protocol.BackupFile{}}
	for _, entry := range entries {
		if err := ctx.Err(); nil != err {
			return err
		}
		if !entry.IsDir() || !stackName.MatchString(entry.Name()) {
			continue
		}
		dir, err := a.stackPath(entry.Name(), false)
		if nil != err {
			return err
		}
		start := len(backup.Files)
		for _, name := range composeNames {
			file, err := backupFile(dir, name)
			if nil != err {
				return err
			}
			if nil != file {
				backup.Files = append(backup.Files, *file)
			}
		}
		if start == len(backup.Files) {
			continue
		}
		file, err := backupFile(dir, ".env")
		if nil != err {
			return err
		}
		if nil != file {
			backup.Files = append(backup.Files, *file)
		}
	}
	respond(w, http.StatusOK, backup)
	return nil
}

func backupFile(dir, name string) (*protocol.BackupFile, error) {
	path, err := managedFile(dir, name)
	if nil != err {
		return nil, fmt.Errorf("stack %s: %w", filepath.Base(dir), err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if nil != err {
		return nil, fmt.Errorf("stack %s: %w", filepath.Base(dir), err)
	}
	return &protocol.BackupFile{Path: filepath.Base(dir) + "/" + name, Data: data}, nil
}
