// Package localapi connects to the local control endpoint without exposing a
// network listener. Unix uses its existing socket; Windows discovers a private
// named pipe through a bounded descriptor in the data directory.
package localapi

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

const descriptorLimit = 4096
const pipePrefix = `\\.\pipe\cc-connect-`

type descriptor struct {
	SchemaVersion int    `json:"schema_version"`
	Transport     string `json:"transport"`
	Address       string `json:"address"`
	InstanceID    string `json:"instance_id"`
	PID           uint32 `json:"pid"`
}

func decodeDescriptor(data []byte) (descriptor, error) {
	var endpoint descriptor
	if len(data) > descriptorLimit {
		return endpoint, fmt.Errorf("local API descriptor exceeds %d bytes", descriptorLimit)
	}
	if !utf8.Valid(data) {
		return endpoint, fmt.Errorf("local API descriptor is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&endpoint); err != nil {
		return endpoint, fmt.Errorf("decode local API descriptor: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return endpoint, fmt.Errorf("local API descriptor has trailing data")
	}
	if endpoint.SchemaVersion != 1 || endpoint.Transport != "named_pipe" || endpoint.PID == 0 {
		return endpoint, fmt.Errorf("unsupported local API descriptor")
	}
	if len(endpoint.InstanceID) != 32 {
		return endpoint, fmt.Errorf("invalid local API instance ID")
	}
	instance, err := hex.DecodeString(endpoint.InstanceID)
	if err != nil || hex.EncodeToString(instance) != endpoint.InstanceID {
		return endpoint, fmt.Errorf("invalid local API instance ID")
	}
	if endpoint.Address != pipePrefix+endpoint.InstanceID {
		return endpoint, fmt.Errorf("local API descriptor does not name its local pipe")
	}
	return endpoint, nil
}
