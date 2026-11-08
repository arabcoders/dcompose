package engine

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateDetachedMounts checks that a helper can share the request and stack files.
func ValidateDetachedMounts(self SelfContainer, requestFile string, stack Stack) error {
	paths := []string{filepath.Dir(requestFile), stack.Dir, stack.ComposeFile}
	if "" != stack.EnvFile && "/dev/null" != stack.EnvFile {
		paths = append(paths, stack.EnvFile)
	}
	for i, path := range paths {
		writable := 0 == i
		var covering *ContainerMount
		for j := range self.Mounts {
			mount := &self.Mounts[j]
			relative, err := filepath.Rel(mount.Destination, path)
			if nil != err || ".." == relative || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			if nil == covering || len(mount.Destination) > len(covering.Destination) {
				covering = mount
			}
		}
		if nil == covering || ("bind" != covering.Type && "volume" != covering.Type) || (writable && !covering.RW) {
			return &Error{Code: 409, Message: "This action requires persistent mounts for the node cache and stack files, with a writable cache. Run it from the host instead.", Cause: fmt.Errorf("path %q is not covered by a shared persistent mount (writable required: %t)", path, writable)}
		}
	}
	return nil
}
