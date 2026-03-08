// Package nmea handles NMEA sentence parsing and GNSS state accumulation.
// Key behaviour per spec section 5:
//   - provider is determined from GSV talkers, NOT from GGA/RMC talker
//   - Quectel EC25 always emits $GPGGA/$GPRMC even in multi-constellation mode
//   - start_type is detected from $GNTXT/$GPTXT or satellite progression
package nmea

import (
	"fmt"
	"strings"
	"time"

	gonmea "github.com/adrianmo/go-nmea"
)

type State struct {
	Fix        bool
	Lat        float64
	Lon        float64
	Alt        float64
	HDOP       float64
	Satellites int
	UTC        time.Time
	StartType  string // hot | warm | cold | unknown

	// GSV talker presence — used for provider detection (spec 5.4)
	gsvTalkers map[string]bool
	// allTalkers — all talker IDs seen
	allTalkers map[string]bool

	fixTime time.Time // wall clock when fix was first acquired
}

func NewState() *State {
	return &State{
		StartType:  "unknown",
		gsvTalkers: make(map[string]bool),
		allTalkers: make(map[string]bool),
	}
}

// Feed parses one raw NMEA line and updates state.
func (s *State) Feed(raw string) error {
	if strings.HasPrefix(raw, "$") {
		talker := extractTalker(raw)
		if talker != "" {
			s.allTalkers[talker] = true
		}
	}

	sentence, err := gonmea.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	switch v := sentence.(type) {
	case gonmea.GGA:
		s.allTalkers[v.TalkerID()] = true
		if v.FixQuality != "0" && v.FixQuality != "" {
			if !s.Fix {
				s.Fix = true
				s.fixTime = time.Now()
			}
			s.Lat = v.Latitude
			s.Lon = v.Longitude
			s.Alt = v.Altitude
			s.HDOP = v.HDOP
			s.Satellites = int(v.NumSatellites)
		}

	case gonmea.RMC:
		s.allTalkers[v.TalkerID()] = true
		if v.Validity == "A" && v.Date.Valid && v.Time.Valid {
			s.UTC = time.Date(
				2000+v.Date.YY, time.Month(v.Date.MM), v.Date.DD,
				v.Time.Hour, v.Time.Minute, v.Time.Second, v.Time.Millisecond*1e6,
				time.UTC,
			)
		}

	case gonmea.GSV:
		s.allTalkers[v.TalkerID()] = true
		s.gsvTalkers[v.TalkerID()] = true

	case gonmea.TXT:
		s.allTalkers[v.TalkerID()] = true
		s.detectStartFromTXT(v.Message)
	}

	return nil
}

// Provider returns the GNSS provider string per spec section 5.4.
// Decision is based on GSV talkers, never on GGA/RMC talker.
func (s *State) Provider() string {
	// Multi-constellation: any non-GPS GSV present
	if s.gsvTalkers["GL"] || s.gsvTalkers["GA"] || s.gsvTalkers["PQ"] {
		return "mixed"
	}
	// u-blox style: GN talker without individual GSV breakdown
	if s.allTalkers["GN"] && !s.gsvTalkers["GP"] {
		return "mixed"
	}
	if s.allTalkers["GL"] {
		return "glonass"
	}
	return "gps"
}

// TimeToFix returns milliseconds from probe start to first fix.
// startTime is the time the probe started reading NMEA.
func (s *State) TimeToFix(startTime time.Time) int64 {
	if !s.Fix || s.fixTime.IsZero() {
		return 0
	}
	return s.fixTime.Sub(startTime).Milliseconds()
}

func (s *State) detectStartFromTXT(msg string) {
	upper := strings.ToUpper(msg)
	switch {
	case strings.Contains(upper, "HOT START"):
		s.StartType = "hot"
	case strings.Contains(upper, "WARM START"):
		s.StartType = "warm"
	case strings.Contains(upper, "COLD START"):
		s.StartType = "cold"
	}
}

// DetectStartFromSatProgression sets start type based on how quickly
// satellites appeared. Call after each GGA if StartType is still "unknown".
func (s *State) DetectStartFromSatProgression(elapsed time.Duration) {
	if s.StartType != "unknown" {
		return
	}
	if s.Satellites > 4 && elapsed < 10*time.Second {
		s.StartType = "hot"
	}
}

func extractTalker(raw string) string {
	// $GPGGA → "GP", $GNGGA → "GN", $PQGSV → "PQ"
	if len(raw) < 3 {
		return ""
	}
	body := raw[1:] // strip $
	if strings.HasPrefix(body, "P") {
		// Proprietary: $PQGSV → talker "PQ"
		if len(body) >= 3 {
			return body[:2]
		}
		return "P"
	}
	if len(body) >= 2 {
		return body[:2]
	}
	return ""
}
