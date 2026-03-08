package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Device    string
	AutoScan  bool
	Baudrate  int
	Mode      string // serial | modem | auto

	ReadDuration    time.Duration
	FixTimeout      time.Duration
	FixTimeoutHot   time.Duration
	FixTimeoutWarm  time.Duration
	FixTimeoutCold  time.Duration
	AssumeColdStart bool

	ScanTimeout    time.Duration
	MinSatellites  int
	DebugNMEA      bool

	AgentID string

	ModemATPort      string
	ModemNMEAPort    string
	ModemBaud        int
	ModemInitTimeout time.Duration

	XTRAEnable   bool
	XTRATimeSync bool
}

func Load() (*Config, error) {
	c := &Config{
		Device:           getEnv("GPS_DEVICE", "auto"),
		AutoScan:         getBool("GPS_AUTO_SCAN", false),
		Baudrate:         getInt("GPS_BAUDRATE", 9600),
		Mode:             getEnv("GPS_MODE", "auto"),
		ReadDuration:     getDuration("GPS_READ_DURATION", 5*time.Second),
		FixTimeout:       getDuration("GPS_FIX_TIMEOUT", 60*time.Second),
		FixTimeoutHot:    getDuration("GPS_FIX_TIMEOUT_HOT", 0),
		FixTimeoutWarm:   getDuration("GPS_FIX_TIMEOUT_WARM", 0),
		FixTimeoutCold:   getDuration("GPS_FIX_TIMEOUT_COLD", 0),
		AssumeColdStart:  getBool("GPS_ASSUME_COLD_START", false),
		ScanTimeout:      getDuration("GPS_SCAN_TIMEOUT", 3*time.Second),
		MinSatellites:    getInt("GPS_MIN_SATELLITES", 4),
		DebugNMEA:        getBool("GPS_DEBUG_NMEA", false),
		AgentID:          getEnv("AGENT_ID", ""),
		ModemATPort:      getEnv("GPS_MODEM_AT_PORT", ""),
		ModemNMEAPort:    getEnv("GPS_MODEM_NMEA_PORT", ""),
		ModemBaud:        getInt("GPS_MODEM_BAUD", 115200),
		ModemInitTimeout: getDuration("GPS_MODEM_INIT_TIMEOUT", 5*time.Second),
		XTRAEnable:       getBool("GPS_XTRA_ENABLE", false),
		XTRATimeSync:     getBool("GPS_XTRA_TIME_SYNC", true),
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	var errs []string

	if c.Baudrate <= 0 {
		errs = append(errs, "GPS_BAUDRATE must be positive")
	}
	if c.ReadDuration <= 0 {
		errs = append(errs, "GPS_READ_DURATION must be positive")
	}
	validModes := map[string]bool{"serial": true, "modem": true, "auto": true}
	if !validModes[c.Mode] {
		errs = append(errs, fmt.Sprintf("GPS_MODE must be serial|modem|auto, got %q", c.Mode))
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// TimeoutFor returns the appropriate fix timeout based on start type.
// Falls back to GPS_FIX_TIMEOUT if type-specific timeout is not set.
func (c *Config) TimeoutFor(startType string) time.Duration {
	switch startType {
	case "hot":
		if c.FixTimeoutHot > 0 {
			return c.FixTimeoutHot
		}
	case "warm":
		if c.FixTimeoutWarm > 0 {
			return c.FixTimeoutWarm
		}
	case "cold":
		if c.FixTimeoutCold > 0 {
			return c.FixTimeoutCold
		}
	}
	return c.FixTimeout
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func getDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
