// Package modem implements Quectel LTE modem GNSS support (spec section 10).
// Discovers AT and NMEA ports via sysfs bInterfaceNumber, sends AT commands,
// reads NMEA from the dedicated NMEA port.
package modem

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	goserial "go.bug.st/serial"
)

const (
	ifNMEA = 1 // bInterfaceNumber for NMEA output port
	ifAT   = 2 // bInterfaceNumber for AT commands port
)

// Ports holds the resolved AT and NMEA serial port paths.
type Ports struct {
	AT   string
	NMEA string
}

// FindPorts discovers AT and NMEA ports for a Quectel modem by VID via sysfs.
// Falls back to probing all ttyUSB* if sysfs bInterfaceNumber is unavailable.
func FindPorts(vid string) (*Ports, error) {
	ports, err := findViaSysfs(vid)
	if err == nil && ports.AT != "" && ports.NMEA != "" {
		return ports, nil
	}
	log.Printf("[WARN] sysfs port detection failed (%v), falling back to probe", err)
	return findViaProbe()
}

// findViaSysfs walks /sys/bus/usb/devices looking for the modem by VID,
// then maps bInterfaceNumber → tty device name.
func findViaSysfs(vid string) (*Ports, error) {
	usbDevices, err := filepath.Glob("/sys/bus/usb/devices/*")
	if err != nil {
		return nil, err
	}

	ports := &Ports{}
	for _, usbDev := range usbDevices {
		devVID, err := readSysfs(filepath.Join(usbDev, "idVendor"))
		if err != nil || !strings.EqualFold(devVID, vid) {
			continue
		}

		// Found the modem — iterate its interfaces
		ifaces, _ := filepath.Glob(usbDev + ":*.*")
		for _, iface := range ifaces {
			ifNumStr, err := readSysfs(filepath.Join(iface, "bInterfaceNumber"))
			if err != nil {
				continue
			}
			var ifNum int
			fmt.Sscanf(ifNumStr, "%d", &ifNum)

			ttyDir, err := filepath.Glob(filepath.Join(iface, "tty", "tty*"))
			if err != nil || len(ttyDir) == 0 {
				continue
			}
			ttyName := "/dev/" + filepath.Base(ttyDir[0])

			switch ifNum {
			case ifNMEA:
				ports.NMEA = ttyName
			case ifAT:
				ports.AT = ttyName
			}
		}

		if ports.AT != "" && ports.NMEA != "" {
			return ports, nil
		}
	}
	return nil, fmt.Errorf("modem with VID %s not found in sysfs", vid)
}

// findViaProbe opens each ttyUSB* and probes: "AT\r\n" → OK = AT port, NMEA lines = NMEA port.
func findViaProbe() (*Ports, error) {
	matches, _ := filepath.Glob("/dev/ttyUSB*")
	if len(matches) == 0 {
		return nil, fmt.Errorf("no ttyUSB devices found")
	}

	ports := &Ports{}
	for _, dev := range matches {
		role := probePortRole(dev, 115200, 2*time.Second)
		switch role {
		case "at":
			if ports.AT == "" {
				ports.AT = dev
			}
		case "nmea":
			if ports.NMEA == "" {
				ports.NMEA = dev
			}
		}
		if ports.AT != "" && ports.NMEA != "" {
			break
		}
	}

	if ports.AT == "" || ports.NMEA == "" {
		return nil, fmt.Errorf("could not identify AT/NMEA ports (AT=%q NMEA=%q)", ports.AT, ports.NMEA)
	}
	return ports, nil
}

func probePortRole(dev string, baud int, timeout time.Duration) string {
	p, err := goserial.Open(dev, &goserial.Mode{BaudRate: baud})
	if err != nil {
		return ""
	}
	defer p.Close()
	p.SetReadTimeout(timeout)

	// Try AT command first
	fmt.Fprintf(p, "AT\r\n")
	scanner := bufio.NewScanner(p)
	deadline := time.Now().Add(timeout)
	for scanner.Scan() && time.Now().Before(deadline) {
		line := strings.TrimSpace(scanner.Text())
		if line == "OK" {
			return "at"
		}
		if strings.HasPrefix(line, "$G") || strings.HasPrefix(line, "$P") {
			return "nmea"
		}
	}
	return ""
}

// AT sends a command and waits for OK or ERROR response.
// Returns the response lines (excluding OK/ERROR) and any error.
func AT(port goserial.Port, cmd string, timeout time.Duration) ([]string, error) {
	port.SetReadTimeout(timeout)
	if _, err := fmt.Fprintf(port, "%s\r\n", cmd); err != nil {
		return nil, fmt.Errorf("write %q: %w", cmd, err)
	}

	var lines []string
	scanner := bufio.NewScanner(port)
	deadline := time.Now().Add(timeout)

	for scanner.Scan() && time.Now().Before(deadline) {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == cmd {
			continue // skip echo
		}
		switch {
		case line == "OK":
			return lines, nil
		case strings.HasPrefix(line, "ERROR"), strings.HasPrefix(line, "+CME ERROR"):
			return lines, fmt.Errorf("%s → %s", cmd, line)
		default:
			lines = append(lines, line)
		}
	}
	return nil, fmt.Errorf("%s: timeout after %s", cmd, timeout)
}

// Open opens a serial port for modem use.
func Open(path string, baud int) (goserial.Port, error) {
	return goserial.Open(path, &goserial.Mode{BaudRate: baud})
}

func readSysfs(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
