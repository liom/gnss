// Package device handles USB device detection via Linux sysfs.
// Reads VID/PID from /sys/class/tty/<dev>/device/../idVendor|idProduct
// and matches against the whitelist from spec section 4.2.
package device

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Info struct {
	Port         string
	VID          string
	PID          string
	Manufacturer string
	Chip         string
	Priority     int
	HasVBAT      bool
	IsModem      bool // Quectel LTE modem — requires modem mode
}

// whitelist maps "VID:PID" → Info (without Port, filled on detection).
var whitelist = map[string]Info{
	"1546:01a7": {Manufacturer: "u-blox", Chip: "NEO-6M", Priority: 100, HasVBAT: false},
	"1546:01a8": {Manufacturer: "u-blox", Chip: "NEO-M8", Priority: 100, HasVBAT: true},
	"1546:01a9": {Manufacturer: "u-blox", Chip: "NEO-9", Priority: 100, HasVBAT: true},
	"0e8d:3329": {Manufacturer: "MediaTek", Chip: "MT3329/MT3339", Priority: 90, HasVBAT: false},
	// Quectel LTE modems — modem mode (AT+QGPS)
	"2c7c:0125": {Manufacturer: "Quectel", Chip: "EC25", Priority: 95, HasVBAT: false, IsModem: true},
	"2c7c:6005": {Manufacturer: "Quectel", Chip: "EC200A", Priority: 95, HasVBAT: false, IsModem: true},
	"2c7c:6001": {Manufacturer: "Quectel", Chip: "EC200U", Priority: 95, HasVBAT: false, IsModem: true},
	"2c7c:6002": {Manufacturer: "Quectel", Chip: "EC200T", Priority: 95, HasVBAT: false, IsModem: true},
	"2c7c:030e": {Manufacturer: "Quectel", Chip: "EM05", Priority: 95, HasVBAT: false, IsModem: true},
	// Generic UART bridges — require NMEA probe
	"10c4:ea60": {Manufacturer: "Silicon Labs", Chip: "CP210x", Priority: 60},
	"0403:6001": {Manufacturer: "FTDI", Chip: "FT232R", Priority: 50},
	"067b:2303": {Manufacturer: "Prolific", Chip: "PL2303", Priority: 50},
	"1a86:7523": {Manufacturer: "QinHeng", Chip: "CH340/CH343", Priority: 40},
}

// Lookup returns device info for a tty port by reading VID/PID from sysfs.
// Returns nil if the device is not in the whitelist or sysfs is unavailable.
func Lookup(port string) *Info {
	devName := filepath.Base(port)
	sysBase := fmt.Sprintf("/sys/class/tty/%s/device", devName)

	vid, err := readSysfs(filepath.Join(sysBase, "../idVendor"))
	if err != nil {
		return nil
	}
	pid, err := readSysfs(filepath.Join(sysBase, "../idProduct"))
	if err != nil {
		return nil
	}

	key := strings.ToLower(vid) + ":" + strings.ToLower(pid)
	if entry, ok := whitelist[key]; ok {
		entry.Port = port
		entry.VID = strings.ToLower(vid)
		entry.PID = strings.ToLower(pid)
		return &entry
	}
	return nil
}

// Scan returns all ttyUSB*/ttyACM* devices sorted by priority (highest first).
// Devices not in the whitelist are appended last with priority 0.
func Scan() ([]*Info, error) {
	var candidates []*Info

	globs := []string{"/dev/ttyUSB*", "/dev/ttyACM*"}
	for _, pattern := range globs {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, port := range matches {
			info := Lookup(port)
			if info == nil {
				info = &Info{Port: port, Priority: 0, Manufacturer: "unknown"}
			}
			candidates = append(candidates, info)
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no serial devices found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})

	return candidates, nil
}

// NeedsNMEAProbe returns true for generic UART bridges that may not be GNSS devices.
func NeedsNMEAProbe(info *Info) bool {
	return info.Priority <= 80
}

func readSysfs(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
