package result

import (
	"encoding/json"
	"os"
)

// ExitCode constants as per spec section 2.
const (
	ExitSuccess     = 0
	ExitNoFix       = 1
	ExitDeviceError = 2
	ExitConfigError = 3
)

type Device struct {
	Port         string `json:"port"`
	Mode         string `json:"mode"`
	AutoDetected bool   `json:"auto_detected"`
	USBVID       string `json:"usb_vid,omitempty"`
	USBPID       string `json:"usb_pid,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Chip         string `json:"chip,omitempty"`
	HasVBAT      *bool  `json:"has_vbat,omitempty"`
}

type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	Alt float64 `json:"alt"`
}

type GNSS struct {
	Fix         bool    `json:"fix"`
	Provider    string  `json:"provider"`
	Satellites  int     `json:"satellites"`
	HDOP        float64 `json:"hdop,omitempty"`
	StartType   string  `json:"start_type"`
	TimeToFixMs int64   `json:"time_to_fix_ms"`
}

type Time struct {
	UTC     string  `json:"utc"`
	DriftMs float64 `json:"drift_ms"`
}

type Result struct {
	Probe         string    `json:"probe"`
	Timestamp     int64     `json:"timestamp"`
	AgentHostname string    `json:"agent_hostname"`
	AgentID       string    `json:"agent_id,omitempty"`
	Device        *Device   `json:"device,omitempty"`
	Location      *Location `json:"location,omitempty"`
	GNSS          *GNSS     `json:"gnss,omitempty"`
	Time          *Time     `json:"time,omitempty"`
	Error         string    `json:"error,omitempty"`
	ErrorCode     int       `json:"error_code,omitempty"`
}

func Emit(r *Result) {
	enc := json.NewEncoder(os.Stdout)
	enc.Encode(r) //nolint:errcheck
}
