package household

import (
	"errors"
	"fmt"
	"time"

	// Embeds the IANA zone database in the binary, so zones load the same
	// way in a minimal image with no /usr/share/zoneinfo.
	_ "time/tzdata"
)

// LoadZone loads the IANA zone name, such as "America/New_York". The empty
// name and "Local", which time.LoadLocation maps to UTC and the process zone,
// are refused: the household zone is never implied.
func LoadZone(name string) (*time.Location, error) {
	if name == "" {
		return nil, errors.New("time zone is empty (want an IANA zone such as America/New_York)")
	}
	if name == "Local" {
		return nil, errors.New(`time zone "Local" is not an IANA zone (want e.g. America/New_York)`)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q (want an IANA zone such as America/New_York)", name)
	}
	return loc, nil
}
