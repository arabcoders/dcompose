package protocol

type BackupFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type NodeBackup struct {
	Files []BackupFile `json:"files"`
	Error string       `json:"error,omitempty"`
}
