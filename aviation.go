// aviation.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type METAR struct {
	airport   string
	time      string
	wind      string
	weather   string
	altimeter string
	rmk       string
}

type NetworkRating int

const (
	UndefinedRating = iota
	ObserverRating
	S1Rating
	S2Rating
	S3Rating
	C1Rating
	C2Rating
	C3Rating
	I1Rating
	I2Rating
	I3Rating
	SupervisorRating
	AdministratorRating
)

func (r NetworkRating) String() string {
	return [...]string{"Undefined", "Observer", "S1", "S2", "S3", "C1", "C2", "C3",
		"I1", "I2", "I3", "Supervisor", "Administrator"}[r]
}

type Facility int

const (
	FacilityOBS = iota
	FacilityFSS
	FacilityDEL
	FacilityGND
	FacilityTWR
	FacilityAPP
	FacilityCTR
	FacilityUndefined
)

func (f Facility) String() string {
	return [...]string{"Observer", "FSS", "Delivery", "Ground", "Tower", "Approach", "Center", "Undefined"}[f]
}

type Frequency float32

func (f Frequency) String() string {
	return fmt.Sprintf("%07.3f", f)
}

type Controller struct {
	callsign string // it's not exactly a callsign, but...
	name     string
	cid      string
	rating   NetworkRating

	frequency  Frequency
	scopeRange int
	facility   Facility
	location   Point2LL

	position *Position
}

type Pilot struct {
	callsign string
	name     string
	cid      string
	rating   NetworkRating
}

type RadarTrack struct {
	position    Point2LL
	altitude    int
	groundspeed int
	heading     float32
}

type FlightRules int

const (
	UNKNOWN = iota
	IFR
	VFR
	DVFR
	SVFR
)

func (f FlightRules) String() string {
	return [...]string{"Unknown", "IFR", "VFR", "DVFR", "SVFR"}[f]
}

type FlightPlan struct {
	callsign       string
	actype         string
	groundspeed    int // what is this used for?
	rules          FlightRules
	depart, arrive string
	alternate      string
	altitude       int
	route          string
	remarks        string
	filed          time.Time
}

func (f *FlightPlan) Filed() bool {
	return !f.filed.IsZero()
}

type FlightStrip struct {
	callsign    string
	formatId    string // ???
	annotations []string
}

type Squawk int

func (s Squawk) String() string { return fmt.Sprintf("%04o", s) }

func ParseSquawk(s string) (Squawk, error) {
	if s == "" {
		return Squawk(0), nil
	}

	sq, err := strconv.ParseInt(s, 8, 32) // base 8!!!
	if err != nil {
		return Squawk(0), fmt.Errorf("%s: invalid squawk code", s)
	} else if sq < 0 || sq > 0o7777 {
		return Squawk(0), fmt.Errorf("%s: out of range squawk code", s)
	}
	return Squawk(sq), nil
}

type Aircraft struct {
	scratchpad         string
	assignedSquawk     Squawk // from ATC
	squawk             Squawk // actually squawking
	mode               TransponderMode
	tempAltitude       int
	voiceCapability    VoiceCapability
	flightPlan         FlightPlan
	trackingController string
	hoController       string // waiting for accept

	tracks    [10]RadarTrack
	firstSeen time.Time
	lastSeen  time.Time // only updated when we get a radar return
}

type AircraftPair struct {
	a, b *Aircraft
}

type TransponderMode int

const (
	Standby = iota
	Charlie
	Ident
)

func (t TransponderMode) String() string {
	return [...]string{"Standby", "C", "Ident"}[t]
}

type RangeLimitFlightRules int

const (
	IFR_IFR = iota
	IFR_VFR
	VFR_VFR
	NumRangeTypes
)

func (r RangeLimitFlightRules) String() string {
	return [...]string{"IFR-IFR", "IFR-VFR", "VFR-VFR"}[r]
}

type RangeLimits struct {
	WarningLateral    float32 // nm
	WarningVertical   int32   // feet
	ViolationLateral  float32
	ViolationVertical int32
}

type Runway struct {
	number         string
	heading        float32
	threshold, end Point2LL
}

type Navaid struct {
	id       string
	navtype  string
	name     string
	location Point2LL
}

type Fix struct {
	id       string
	location Point2LL
}

type PRDEntry struct {
	Depart, Arrive          string
	Route                   string
	Hours                   [3]string
	Type                    string
	Area                    string
	Altitude                string
	Aircraft                string
	Direction               string
	Seq                     string
	DepCenter, ArriveCenter string
}

type AirportPair struct {
	depart, arrive string
}

type Airport struct {
	id        string
	name      string
	elevation int
	location  Point2LL
}

type Callsign struct {
	company   string
	country   string
	telephony string
	threeltr  string
}

type Position struct {
	name                  string // e.g., Kennedy Local 1
	callsign              string // e.g., Kennedy Tower
	frequency             Frequency
	sectorId              string // For handoffs, etc--e.g., 2W
	scope                 string // For tracked a/c on the scope--e.g., T
	id                    string // e.g. JFK_TWR
	lowSquawk, highSquawk Squawk
}

type User struct {
	name   string
	note   string
	rating NetworkRating
}

type VoiceCapability int

const (
	Unknown = iota
	Voice
	Receive
	Text
)

func (v VoiceCapability) String() string {
	return [...]string{"?", "v", "r", "t"}[v]
}

type TextMessageType int

const (
	TextBroadcast = iota
	TextWallop
	TextATC
	TextFrequency
	TextPrivate
)

func (t TextMessageType) String() string {
	return [...]string{"Broadcast", "Wallop", "ATC", "Frequency", "Private"}[t]
}

type TextMessage struct {
	sender      string
	messageType TextMessageType
	contents    string
	frequency   Frequency // only used for messageType == TextFrequency
}

func (a *Aircraft) Altitude() int {
	return a.tracks[0].altitude
}

func (a *Aircraft) HaveTrack() bool {
	return a.Position()[0] != 0 || a.Position()[1] != 0
}

func (a *Aircraft) Position() Point2LL {
	return a.tracks[0].position
}

func (a *Aircraft) GroundSpeed() int {
	return a.tracks[0].groundspeed
}

// Note: returned value includes the magnetic correction
func (a *Aircraft) Heading() float32 {
	// The heading reported by vatsim seems systemically off for some (but
	// not all!) aircraft and not just magnetic variation. So take the
	// heading vector, which is more reliable, and work from there...
	return headingv2ll(a.HeadingVector(), world.MagneticVariation)
}

func (a *Aircraft) HeadingVector() Point2LL {
	p0 := a.tracks[0].position
	p1 := a.tracks[1].position
	return Point2LL{p0[0] - p1[0], p0[1] - p1[1]}
}

func (a *Aircraft) HaveHeading() bool {
	return !a.tracks[0].position.IsZero() && !a.tracks[1].position.IsZero()
}

func (a *Aircraft) ExtrapolatedHeadingVector() Point2LL {
	// fit a parabola to the last three points, then return the tangent at p[0]
	// a x^2 + b x + c = y
	// x in [0,1,2], y last 3 latitudes / longitudes, respectively
	// form the matrix:
	// [x0^2  x0 1 ] [a]   [y0]
	// [x1^2  x1 1 ] [b] = [y1]
	// [x2^2  x2 1 ] [c]   [y2]
	//
	// or:
	// [0 0 1] [a]   [y0]
	// [1 1 1] [b] = [y1]
	// [4 2 1] [c]   [y2]
	//
	// solving gives:
	// a = 1/2 (y0 - 2 y1 + y2)
	// b = 1/2 (-3 y0 + 4y1 - y2)
	// c = y0
	//
	// The derivative of the parabola is:
	// 1/2 (-3 y0 + 4 y1 - y2) + x (y0 - 2 y1 + y2)
	//
	// evaluated at x=0 we finally have:
	// 1/2 (-3 y0 + 4 y1 - y2)
	tangent0 := func(p0, p1, p2 float32) float32 {
		return 0.5 * (-3*p0 + 4*p1 - p2)
	}
	p0 := a.tracks[0].position
	p1 := a.tracks[1].position
	p2 := a.tracks[2].position
	if p2.IsZero() {
		// not enough data yet
		return Point2LL{}
	}
	// Negate the tangent since we want to be considering going away from p0
	return Point2LL{-tangent0(p0[0], p1[0], p2[0]), -tangent0(p0[1], p1[1], p2[1])}
}

func (a *Aircraft) HeadingTo(p Point2LL) float32 {
	return headingp2ll(a.Position(), p, world.MagneticVariation)
}

func (a *Aircraft) LostTrack() bool {
	d := time.Since(a.lastSeen)
	return d > 15*time.Second
}

func (a *Aircraft) Callsign() string {
	return a.flightPlan.callsign
}

func (a *Aircraft) OnGround() bool {
	if a.GroundSpeed() < 40 {
		return true
	}

	for _, airport := range [2]string{a.flightPlan.depart, a.flightPlan.arrive} {
		if ap, ok := world.FAA.airports[airport]; ok {
			heightAGL := abs(a.Altitude() - ap.elevation)
			return heightAGL < 100
		}
	}
	// Didn't know of the airports. Most likely no flight plan has been
	// filed. We could be more fancy and find the closest airport in the
	// sector file and then use its elevation, though it's not clear that
	// is worth the work.
	return false
}

func (a *Aircraft) GetFormattedFlightPlan(includeRemarks bool) (contents string, indent int) {
	if a.Callsign() == "" {
		contents = "No flight plan"
		return
	} else {
		plan := a.flightPlan

		var sb strings.Builder
		w := tabwriter.NewWriter(&sb, 0, 1, 1, ' ', 0)
		write := func(s string) { w.Write([]byte(s)) }

		write(a.Callsign())
		if a.voiceCapability != Voice {
			write("/" + a.voiceCapability.String())
		}
		write("\t")
		write("rules: " + plan.rules.String() + "\t")
		write("a/c: " + plan.actype + "\t")
		write("dep/arr: " + plan.depart + "-" + plan.arrive + " (" + plan.alternate + ")\n")

		write("\t")
		write("alt:   " + fmt.Sprintf("%d", plan.altitude))
		if a.tempAltitude != 0 {
			write(fmt.Sprintf(" (%d)", a.tempAltitude))
		}
		write("\t")
		write("sqk: " + a.assignedSquawk.String() + "\t")
		write("scratch: " + a.scratchpad + "\n")

		w.Flush()
		contents = sb.String()

		indent = 1 + len(a.Callsign())
		if a.voiceCapability != Voice {
			indent += 1 + len(a.voiceCapability.String())
		}
		indstr := fmt.Sprintf("%*c", indent, ' ')
		contents = contents + indstr + "route: " + plan.route + "\n"
		if includeRemarks {
			contents = contents + indstr + "rmks:  " + plan.remarks + "\n"
		}

		return contents, indent
	}
}

// Returns nm
func EstimatedFutureDistance(a *Aircraft, b *Aircraft, seconds float32) float32 {
	a0, av := a.Position(), a.HeadingVector()
	b0, bv := b.Position(), b.HeadingVector()
	afut := add2f(a0, scale2f(av, seconds/5)) // assume 5s per track
	bfut := add2f(b0, scale2f(bv, seconds/5)) // ditto..
	return nmdistance2ll(afut, bfut)
}

func TrafficCall(from *Aircraft, to *Aircraft) string {
	frompos := from.tracks[0]
	topos := to.tracks[0]

	alt := (topos.altitude + 250) / 500 * 500
	// Include magnetic correction in hto so that it cancels when we
	// subtract from from.Heading().
	hto := headingp2ll(frompos.position, topos.position, world.MagneticVariation)
	clock := headingAsHour(hto - from.Heading())
	dist := nmdistance2ll(frompos.position, topos.position)

	return fmt.Sprintf("  %-10s %2d o'c %2d mi %2s bound %-10s %5d'\n",
		from.Callsign(), clock, int(dist+0.5),
		shortCompass(to.Heading()), to.flightPlan.actype, int(alt))
}

func (fp FlightPlan) TypeWithoutSuffix() string {
	// try to chop off equipment suffix
	actypeFields := strings.Split(fp.actype, "/")
	switch len(actypeFields) {
	case 3:
		// Heavy (presumably), with suffix
		return actypeFields[0] + "/" + actypeFields[1]
	case 2:
		if actypeFields[0] == "H" || actypeFields[0] == "S" {
			// Heavy or super, no suffix
			return actypeFields[0] + "/" + actypeFields[1]
		} else {
			// No heavy, with suffix
			return actypeFields[0]
		}
	default:
		// Who knows, so leave it alone
		return fp.actype
	}
}

func (fp FlightPlan) Telephony() string {
	cs := strings.TrimRight(fp.callsign, "0123456789")
	if sign, ok := world.callsigns[cs]; ok {
		return sign.telephony
	} else {
		return ""
	}
}
