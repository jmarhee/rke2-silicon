package gpu

import (
	"encoding/json"
	"os/exec"
)

// Device is the host GPU the broker will run kernels on.
type Device struct {
	Name    string `json:"name,omitempty"`
	Cores   string `json:"cores,omitempty"`
	Present bool   `json:"present"`
}

// Probe reads the builtin GPU from system_profiler.
func Probe() Device {
	out, err := exec.Command("system_profiler", "SPDisplaysDataType", "-json").Output()
	if err != nil {
		return Device{}
	}
	dev, err := ParseDisplays(out)
	if err != nil {
		return Device{}
	}
	return dev
}

// ParseDisplays finds the builtin GPU in system_profiler JSON.
func ParseDisplays(data []byte) (Device, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Device{}, err
	}
	list, ok := raw["SPDisplaysDataType"]
	if !ok {
		return Device{}, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(list, &entries); err != nil {
		return Device{}, err
	}
	for _, entry := range entries {
		model := jsonString(entry["sppci_model"])
		if model == "" {
			continue
		}
		return Device{
			Name:    model,
			Cores:   jsonString(entry["sppci_cores"]),
			Present: true,
		}, nil
	}
	return Device{}, nil
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
