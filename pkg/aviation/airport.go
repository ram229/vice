// pkg/aviation/airport.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

type Airport struct {
	Location       math.Point2LL
	TowerListIndex int `json:"tower_list"`

	Name string `json:"name"`

	Approaches map[string]*Approach `json:"approaches,omitempty"`
	Departures []Departure          `json:"departures,omitempty"`

	VFR struct {
		Randoms VFRRandomsSpec `json:"random_routes"`
		Routes  []VFRRouteSpec `json:"routes"`
	} `json:"vfr"`

	// Optional: initial tracking controller, for cases where a virtual
	// controller has the initial track.
	DepartureController string `json:"departure_controller"`
	HoldForRelease      bool   `json:"hold_for_release"`

	ExitCategories map[string]string `json:"exit_categories"`

	// runway -> (exit -> route)
	DepartureRoutes map[string]map[string]*ExitRoute `json:"departure_routes"`

	ApproachRegions   map[string]*ApproachRegion `json:"approach_regions"`
	ConvergingRunways []ConvergingRunways        `json:"converging_runways"`

	ATPAVolumes           map[string]*ATPAVolume `json:"atpa_volumes"`
	OmitArrivalScratchpad bool                   `json:"omit_arrival_scratchpad"`
	DepartureRunwaysAsOne []string               `json:"departure_runways_as_one"`
}

type VFRRandomsSpec struct {
	Rate  int    `json:"rate"`
	Fleet string `json:"fleet"`
}

type VFRRouteSpec struct {
	Name        string        `json:"name"`
	Rate        int           `json:"rate"`
	Fleet       string        `json:"fleet"`
	Waypoints   WaypointArray `json:"waypoints"`
	Destination string        `json:"destination"`
	Description string        `json:"description"`
}

type ConvergingRunways struct {
	Runways                [2]string                        `json:"runways"`
	TieSymbol              string                           `json:"tie_symbol"`
	StaggerSymbol          string                           `json:"stagger_symbol"`
	TieOffset              float32                          `json:"tie_offset"`
	LeaderDirectionStrings [2]string                        `json:"leader_directions"`
	LeaderDirections       [2]math.CardinalOrdinalDirection // not in JSON, set during deserialize
	RunwayIntersection     math.Point2LL                    // not in JSON, set during deserialize
}

type ApproachRegion struct {
	Runway           string  // set during deserialization
	HeadingTolerance float32 `json:"heading_tolerance"`

	ReferenceLineHeading   float32       `json:"reference_heading"`
	ReferenceLineLength    float32       `json:"reference_length"`
	ReferencePointAltitude float32       `json:"reference_altitude"`
	ReferencePoint         math.Point2LL `json:"reference_point"`

	// lateral qualification region
	NearDistance  float32 `json:"near_distance"`
	NearHalfWidth float32 `json:"near_half_width"`
	FarHalfWidth  float32 `json:"far_half_width"`
	RegionLength  float32 `json:"region_length"`

	// vertical qualification region
	DescentPointDistance   float32 `json:"descent_distance"`
	DescentPointAltitude   float32 `json:"descent_altitude"`
	AboveAltitudeTolerance float32 `json:"above_altitude_tolerance"`
	BelowAltitudeTolerance float32 `json:"below_altitude_tolerance"`

	ScratchpadPatterns []string `json:"scratchpad_patterns"`
}

type ATPAVolume struct {
	Id                  string `json:"id"`
	ThresholdString     string `json:"runway_threshold"`
	Threshold           math.Point2LL
	Heading             float32  `json:"heading"`
	MaxHeadingDeviation float32  `json:"max_heading_deviation"`
	Floor               float32  `json:"floor"`
	Ceiling             float32  `json:"ceiling"`
	Length              float32  `json:"length"`
	LeftWidth           float32  `json:"left_width"`
	RightWidth          float32  `json:"right_width"`
	FilteredScratchpads []string `json:"filtered_scratchpads"`
	ExcludedScratchpads []string `json:"excluded_scratchpads"`
	Enable25nmApproach  bool     `json:"enable_2.5nm"`
	Dist25nmApproach    float32  `json:"2.5nm_distance"`
}

// returns a point along the reference line with given distance from the
// reference point, in nm coordinates.
func (ar *ApproachRegion) referenceLinePoint(dist, nmPerLongitude, magneticVariation float32) [2]float32 {
	hdg := math.Radians(ar.ReferenceLineHeading + 180 - magneticVariation)
	v := [2]float32{math.Sin(hdg), math.Cos(hdg)}
	pref := math.LL2NM(ar.ReferencePoint, nmPerLongitude)
	return math.Add2f(pref, math.Scale2f(v, dist))
}

func (ar *ApproachRegion) NearPoint(nmPerLongitude, magneticVariation float32) [2]float32 {
	return ar.referenceLinePoint(ar.NearDistance, nmPerLongitude, magneticVariation)
}

func (ar *ApproachRegion) FarPoint(nmPerLongitude, magneticVariation float32) [2]float32 {
	return ar.referenceLinePoint(ar.NearDistance+ar.RegionLength, nmPerLongitude, magneticVariation)
}

func (ar *ApproachRegion) GetLateralGeometry(nmPerLongitude, magneticVariation float32) (line [2]math.Point2LL, quad [4]math.Point2LL) {
	// Start with the reference line
	p0 := ar.referenceLinePoint(0, nmPerLongitude, magneticVariation)
	p1 := ar.referenceLinePoint(ar.ReferenceLineLength, nmPerLongitude, magneticVariation)
	line = [2]math.Point2LL{math.NM2LL(p0, nmPerLongitude), math.NM2LL(p1, nmPerLongitude)}

	// Get the unit vector perpendicular to the reference line
	v := math.Normalize2f(math.Sub2f(p1, p0))
	vperp := [2]float32{-v[1], v[0]}

	pNear := ar.referenceLinePoint(ar.NearDistance, nmPerLongitude, magneticVariation)
	pFar := ar.referenceLinePoint(ar.NearDistance+ar.RegionLength, nmPerLongitude, magneticVariation)
	q0 := math.Add2f(pNear, math.Scale2f(vperp, ar.NearHalfWidth))
	q1 := math.Add2f(pFar, math.Scale2f(vperp, ar.FarHalfWidth))
	q2 := math.Add2f(pFar, math.Scale2f(vperp, -ar.FarHalfWidth))
	q3 := math.Add2f(pNear, math.Scale2f(vperp, -ar.NearHalfWidth))
	quad = [4]math.Point2LL{math.NM2LL(q0, nmPerLongitude), math.NM2LL(q1, nmPerLongitude),
		math.NM2LL(q2, nmPerLongitude), math.NM2LL(q3, nmPerLongitude)}

	return
}

type GhostAircraft struct {
	Callsign            string
	Position            math.Point2LL
	Groundspeed         int
	LeaderLineDirection math.CardinalOrdinalDirection
	TrackId             string
}

func (ar *ApproachRegion) Inside(p math.Point2LL, alt float32, nmPerLongitude, magneticVariation float32) (lateral, vertical bool) {
	line, quad := ar.GetLateralGeometry(nmPerLongitude, magneticVariation)
	lateral = math.PointInPolygon2LL(p, quad[:])

	// Work in nm here...
	l := [2][2]float32{math.LL2NM(line[0], nmPerLongitude), math.LL2NM(line[1], nmPerLongitude)}
	pc := math.ClosestPointOnLine(l, math.LL2NM(p, nmPerLongitude))
	d := math.Distance2f(pc, l[0])
	if d > ar.DescentPointDistance {
		vertical = alt <= ar.DescentPointAltitude+ar.AboveAltitudeTolerance &&
			alt >= ar.DescentPointAltitude-ar.BelowAltitudeTolerance
	} else {
		t := (d - ar.NearDistance) / (ar.DescentPointDistance - ar.NearDistance)
		approachAlt := math.Lerp(t, ar.ReferencePointAltitude, ar.DescentPointAltitude)
		vertical = alt <= approachAlt+ar.AboveAltitudeTolerance &&
			alt >= alt-ar.BelowAltitudeTolerance
	}
	return
}

func (ar *ApproachRegion) TryMakeGhost(callsign string, track RadarTrack, heading float32, scratchpad string,
	forceGhost bool, offset float32, leaderDirection math.CardinalOrdinalDirection, runwayIntersection [2]float32,
	nmPerLongitude float32, magneticVariation float32, other *ApproachRegion) *GhostAircraft {
	// Start with lateral extent since even if it's forced, the aircraft still must be inside it.
	lat, vert := ar.Inside(track.Position, float32(track.Altitude), nmPerLongitude, magneticVariation)
	if !lat {
		return nil
	}

	if !forceGhost {
		// Heading must be in range
		if math.HeadingDifference(heading, ar.ReferenceLineHeading) > ar.HeadingTolerance {
			return nil
		}

		// Check vertical extent
		if !vert {
			return nil
		}

		if len(ar.ScratchpadPatterns) > 0 {
			if !slices.ContainsFunc(ar.ScratchpadPatterns,
				func(pat string) bool { return strings.Contains(scratchpad, pat) }) {
				return nil
			}
		}
	}

	isectNm := math.LL2NM(runwayIntersection, nmPerLongitude)
	remap := func(pll math.Point2LL) math.Point2LL {
		// Switch to nm for transformations to compute ghost position
		p := math.LL2NM(pll, nmPerLongitude)
		// Vector to reference point
		v := math.Sub2f(p, isectNm)
		// Rotate it to be oriented with respect to the other runway's reference point
		v = math.Rotator2f(other.ReferenceLineHeading - ar.ReferenceLineHeading)(v)
		// Offset as appropriate
		v = math.Add2f(v, math.Scale2f(math.Normalize2f(v), offset))
		// Back to a nm point with regards to the other reference point
		p = math.Add2f(isectNm, v)
		// And lat-long for the final result
		return math.NM2LL(p, nmPerLongitude)
	}

	ghost := &GhostAircraft{
		Callsign:            callsign,
		Position:            remap(track.Position),
		Groundspeed:         track.Groundspeed,
		LeaderLineDirection: leaderDirection,
	}

	return ghost
}

func (a *ATPAVolume) Inside(p math.Point2LL, alt, hdg, nmPerLongitude, magneticVariation float32) bool {
	if alt < a.Floor || alt > a.Ceiling {
		return false
	}
	if math.HeadingDifference(hdg, a.Heading) > a.MaxHeadingDeviation {
		return false
	}

	rect := a.GetRect(nmPerLongitude, magneticVariation)
	return math.PointInPolygon2LL(p, rect[:])
}

func (a *ATPAVolume) GetRect(nmPerLongitude, magneticVariation float32) [4]math.Point2LL {
	// Segment along the approach course
	p0 := math.LL2NM(a.Threshold, nmPerLongitude)
	hdg := a.Heading - magneticVariation + 180
	v := [2]float32{math.Sin(math.Radians(hdg)), math.Cos(math.Radians(hdg))}
	p1 := math.Add2f(p0, math.Scale2f(v, a.Length))

	vp := [2]float32{-v[1], v[0]} // perp
	left, right := a.LeftWidth/math.NauticalMilesToFeet, a.RightWidth/math.NauticalMilesToFeet

	quad := [4][2]float32{
		math.Add2f(p0, math.Scale2f(vp, -left)), math.Add2f(p1, math.Scale2f(vp, -left)),
		math.Add2f(p1, math.Scale2f(vp, right)), math.Add2f(p0, math.Scale2f(vp, right))}
	return [4]math.Point2LL{
		math.NM2LL(quad[0], nmPerLongitude), math.NM2LL(quad[1], nmPerLongitude),
		math.NM2LL(quad[2], nmPerLongitude), math.NM2LL(quad[3], nmPerLongitude)}
}

func (ap *Airport) PostDeserialize(icao string, loc Locator, nmPerLongitude float32,
	magneticVariation float32, controlPositions map[string]*Controller, scratchpads map[string]string,
	facilityAirports map[string]*Airport, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if info, ok := DB.Airports[icao]; !ok {
		e.ErrorString("airport %q not found in airport database", icao)
	} else {
		ap.Location = info.Location

		if len(info.Runways) == 0 {
			e.ErrorString("no runways found at %q", icao)
		}
	}

	if ap.Location.IsZero() {
		e.ErrorString("Must specify \"location\" for airport")
	}

	for name, appr := range ap.Approaches {
		e.Push("Approach " + name)

		if util.IsAllNumbers(name) {
			e.ErrorString("Approach names cannot only have numbers in them")
		}

		if appr.Id != "" {
			if dbAppr, ok := DB.Airports[icao].Approaches[appr.Id]; !ok {
				e.ErrorString("Approach %q not in database. Options: %s", appr.Id,
					strings.Join(util.SortedMapKeys(DB.Airports[icao].Approaches), ", "))
				e.Pop()
				continue
			} else {
				// Copy the approach from the database
				appr.Type = dbAppr.Type
				if appr.Runway == "" {
					appr.Runway = dbAppr.Runway
				}
				if appr.ApproachHeading == 0 {
					appr.ApproachHeading = dbAppr.ApproachHeading
				}

				// This is a little hacky, but we'll duplicate the waypoint
				// arrays since later we e.g., append a waypoint for the
				// runway threshold.  This leads to errors if a CIFP
				// approach is referenced in two scenario files.
				for _, wps := range dbAppr.Waypoints {
					appr.Waypoints = append(appr.Waypoints, util.DuplicateSlice(wps))
				}
			}
		}

		if appr.Runway == "" {
			e.ErrorString("Must specify \"runway\"")
		}
		rwy, ok := LookupRunway(icao, appr.Runway)
		if !ok {
			e.ErrorString("\"runway\" %q is unknown. Options: %s", appr.Runway,
				DB.Airports[icao].ValidRunways())
		}

		for i := range appr.Waypoints {
			appr.Waypoints[i].InitializeLocations(loc, nmPerLongitude, magneticVariation, e)

			// Add the final fix at the runway threshold.
			appr.Waypoints[i] = append(appr.Waypoints[i], Waypoint{
				Fix:      "_" + appr.Runway + "_THRESHOLD",
				Location: rwy.Threshold,
				AltitudeRestriction: &AltitudeRestriction{
					Range: [2]float32{float32(rwy.Elevation), float32(rwy.Elevation)},
				},
				Land:    true,
				FlyOver: true,
			})
			n := len(appr.Waypoints[i])

			if appr.Waypoints[i][n-1].ProcedureTurn != nil {
				e.ErrorString("ProcedureTurn cannot be specified at the final waypoint")
			}
			for j, wp := range appr.Waypoints[i] {
				appr.Waypoints[i][j].OnApproach = true
				e.Push("Fix " + wp.Fix)
				if wp.NoPT {
					if !slices.ContainsFunc(appr.Waypoints[i][j+1:],
						func(wp Waypoint) bool { return wp.ProcedureTurn != nil }) {
						e.ErrorString("No procedure turn found after fix with \"nopt\"")
					}
				}
				e.Pop()
			}
		}
		requireFAF := appr.Type != ChartedVisualApproach
		CheckApproaches(e, appr.Waypoints, requireFAF, controlPositions)

		if appr.FullName == "" {
			if appr.Type == ChartedVisualApproach {
				e.ErrorString("Must provide \"full_name\" for charted visual approach")
			} else {
				appr.FullName = appr.Type.String() + " "
				if len(appr.Id) > 3 && appr.Id[1] >= 'W' && appr.Id[1] <= 'Z' {
					appr.FullName += string(appr.Id[1]) + " "
				}
				if len(appr.Id) > 3 && appr.Id[0] == 'G' {
					appr.FullName += "GPS "
				}
				appr.FullName += "Runway " + appr.Runway
			}
		} else if !strings.Contains(appr.FullName, "runway") && !strings.Contains(appr.FullName, "Runway") {
			e.ErrorString("Must have \"runway\" in approach's \"full_name\"")
		}

		if appr.Type == ChartedVisualApproach && len(appr.Waypoints) != 1 {
			// Note: this could be relaxed if necessary but the logic in
			// Nav prepareForChartedVisual() assumes as much.
			e.ErrorString("Only a single set of waypoints are allowed for a charted visual approach route")
		}

		e.Pop()
	}

	if _, ok := controlPositions[ap.DepartureController]; !ok && ap.DepartureController != "" {
		e.ErrorString("departure_controller %q unknown", ap.DepartureController)
	}

	// Departure routes are specified in the JSON as comma-separated lists
	// of exits. We'll split those out into individual entries in the
	// Airport's DepartureRoutes, one per exit, for convenience of future code.
	splitDepartureRoutes := make(map[string]map[string]*ExitRoute)
	for rwy, rwyRoutes := range ap.DepartureRoutes {
		e.Push("Departure runway " + rwy)
		seenExits := make(map[string]interface{})
		splitDepartureRoutes[rwy] = make(map[string]*ExitRoute)

		r, ok := LookupRunway(icao, rwy)
		if !ok {
			e.ErrorString("unknown runway for airport")
		}
		rend, ok := LookupOppositeRunway(icao, rwy)
		if !ok {
			e.ErrorString("missing opposite runway")
		}

		for exitList, route := range rwyRoutes {
			e.Push("Exit " + exitList)
			route.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, e)

			route.Waypoints = append([]Waypoint{
				Waypoint{
					Fix:      rwy,
					Location: r.Threshold,
				},
				Waypoint{
					Fix:      rwy + "-mid",
					Location: math.Lerp2f(0.75, r.Threshold, rend.Threshold),
				}}, route.Waypoints...)

			for i := range route.Waypoints {
				route.Waypoints[i].OnSID = true
			}

			route.Waypoints.CheckDeparture(e, controlPositions)

			for _, exit := range strings.Split(exitList, ",") {
				exit = strings.TrimSpace(exit)
				if _, ok := seenExits[exit]; ok {
					e.ErrorString("%s: exit repeatedly specified in routes", exit)
				}
				seenExits[exit] = nil

				splitDepartureRoutes[rwy][exit] = route
			}
			e.Pop()
		}
		e.Pop()
	}
	ap.DepartureRoutes = splitDepartureRoutes

	for rwy, routes := range ap.DepartureRoutes {
		e.Push("Departure runway " + rwy)
		for exit, route := range routes {
			e.Push("Exit " + exit)

			if slices.ContainsFunc(route.Waypoints, func(wp Waypoint) bool { return wp.HumanHandoff }) {
				if route.HandoffController == "" {
					e.ErrorString("no \"handoff_controller\" specified even though route has \"/ho\"")
				} else if _, ok := controlPositions[route.HandoffController]; !ok {
					e.ErrorString("control position %q unknown in scenario", route.HandoffController)
				}
			} else if route.HandoffController != "" {
				e.ErrorString("\"handoff_controller\" specified but won't be used since route has no \"/ho\"")
			}

			if route.AssignedAltitude == 0 && route.ClearedAltitude == 0 {
				e.ErrorString("must specify either \"assigned_altitude\" or \"cleared_altitude\"")
			} else if route.AssignedAltitude != 0 && route.ClearedAltitude != 0 {
				e.ErrorString("cannot specify both \"assigned_altitude\" and \"cleared_altitude\"")
			}

			e.Pop()
		}
		e.Pop()
	}

	for i, dep := range ap.Departures {
		e.Push("Departure exit " + dep.Exit)
		e.Push("Destination " + dep.Destination)

		for _, alt := range dep.Altitudes {
			if alt < 500 {
				e.ErrorString("altitude of %v is too low to be used. Is it supposed to be %v?", alt, alt*100)
			}
		}

		if _, ok := DB.Airports[dep.Destination]; !ok {
			e.ErrorString("destination airport %q unknown", dep.Destination)
		}

		if len(dep.Airlines) == 0 {
			e.ErrorString("No \"airlines\" specified for departure")
		}

		// Make sure that all runways have a route to the exit
		for rwy := range ap.DepartureRoutes {
			if _, ok := LookupRunway(icao, rwy); !ok {
				e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[icao].ValidRunways())
			}
		}

		// We may have multiple ways to reach an exit (e.g. different for
		// jets vs piston aircraft); in that case the departure exit may be
		// specified like COLIN.P, etc.  Therefore, here we remove any
		// trailing non-alphabetical characters for the departure exit name
		// used below.
		depExit := dep.Exit
		for i, ch := range depExit {
			if !unicode.IsLetter(ch) {
				depExit = depExit[:i]
				break
			}
		}

		if _, intraFacility := facilityAirports[dep.Destination]; intraFacility {
			// Make sure that the full route is valid.
			wp, err := parseWaypoints(dep.Route)
			if err != nil {
				e.Error(err)
			}
			wp.InitializeLocations(loc, nmPerLongitude, magneticVariation, e)
			ap.Departures[i].RouteWaypoints = wp
		} else {
			for _, fix := range strings.Fields(dep.Route) {
				wp := WaypointArray{Waypoint{Fix: fix}}
				// Best effort only to find waypoint locations; this will fail
				// for airways, international ones not in the FAA database,
				// latlongs in the flight plan, etc.
				if fix == depExit {
					wp.InitializeLocations(loc, nmPerLongitude, magneticVariation, e)
				} else {
					// nil here so errors aren't logged if it's not the actual exit.
					wp.InitializeLocations(loc, nmPerLongitude, magneticVariation, nil)
				}
				if !wp[0].Location.IsZero() {
					ap.Departures[i].RouteWaypoints = append(ap.Departures[i].RouteWaypoints, wp[0])
				}
			}
		}

		if !slices.ContainsFunc(ap.Departures[i].RouteWaypoints,
			func(wp Waypoint) bool { return wp.Fix == depExit }) {
			e.ErrorString("exit %q not found in departure route", depExit)
		}

		for _, al := range dep.Airlines {
			al.Check(e)
		}

		e.Pop()
		e.Pop()
	}

	ga := DB.Airlines["N"]
	checkFleet := func(fleet, loc string) {
		if fleet == "" {
			return
		}
		if _, ok := ga.Fleets[fleet]; !ok {
			e.ErrorString("Fleet %q in %q is not a valid GA aircraft fleet. Options: %s",
				fleet, loc, strings.Join(slices.Collect(maps.Keys(ga.Fleets)), ", "))
		}
	}
	e.Push("\"vfr\"")
	if ap.VFR.Randoms.Fleet != "" {
		checkFleet(ap.VFR.Randoms.Fleet, "random_routes")
		if ap.VFR.Randoms.Rate == 0 {
			e.ErrorString("\"fleet\" specified for \"vfr\" \"random_routes\" but \"rate\" is not specified or is zero.")
		}
	}
	for i := range ap.VFR.Routes {
		ap.VFR.Routes[i].Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, e)

		spec := &ap.VFR.Routes[i]
		e.Push("routes " + spec.Name)
		if spec.Rate == 0 {
			e.ErrorString("No \"rate\" specified")
		}
		if spec.Fleet == "" {
			spec.Fleet = "default"
		} else {
			checkFleet(spec.Fleet, "routes")
		}
		if len(spec.Waypoints) == 0 {
			e.ErrorString("must specify \"waypoints\"")
		} else {
			spec.Waypoints[len(spec.Waypoints)-1].Land = true
		}
		if _, ok := DB.Airports[spec.Destination]; !ok {
			e.ErrorString("Destination airport %q unknown", spec.Destination)
		}
		e.Pop()
	}
	e.Pop()

	// Check if airport has VFR departures but is in class B or C airspace
	if ap.VFR.Randoms.Rate > 0 || len(ap.VFR.Routes) > 0 {
		elevation := DB.Airports[icao].Elevation
		checkAllVolumes := func(volsIter iter.Seq[[]AirspaceVolume]) bool {
			return util.SeqContainsFunc(volsIter, func(vols []AirspaceVolume) bool {
				return slices.ContainsFunc(vols, func(vol AirspaceVolume) bool {
					return vol.Inside(ap.Location, elevation)
				})
			})
		}
		if checkAllVolumes(maps.Values(DB.BravoAirspace)) || checkAllVolumes(maps.Values(DB.CharlieAirspace)) {
			e.ErrorString("Airport has VFR departures specified but is located in class B or C airspace")
		}
	}

	// Validate DepartureRunwaysAsOne entries
	seenRunways := make(map[string]bool)
	for i, group := range ap.DepartureRunwaysAsOne {
		e.Push(fmt.Sprintf("departure_runways_as_one[%d]", i))
		runways := strings.Split(group, ",")
		if len(runways) < 2 {
			e.ErrorString("must specify at least two runways")
		}
		for _, rwy := range runways {
			rwy = strings.TrimSpace(rwy)
			if _, ok := LookupRunway(icao, rwy); !ok {
				e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[icao].ValidRunways())
			}
			if seenRunways[rwy] {
				e.ErrorString("runway %q appears in multiple groups", rwy)
			}
			seenRunways[rwy] = true
		}
		e.Pop()
	}

	for rwy, def := range ap.ApproachRegions {
		e.Push(rwy + " region")
		def.Runway = rwy

		if _, ok := LookupRunway(icao, rwy); !ok {
			e.ErrorString("runway %q is unknown. Options: %s", rwy,
				DB.Airports[icao].ValidRunways())
		}

		if !slices.ContainsFunc(ap.ConvergingRunways,
			func(c ConvergingRunways) bool { return c.Runways[0] == rwy || c.Runways[1] == rwy }) {
			e.ErrorString("runway not used in \"converging_runways\"")
		}

		e.Pop()
	}

	for i, pair := range ap.ConvergingRunways {
		e.Push("Converging runways " + pair.Runways[0] + "/" + pair.Runways[1])

		for _, rwy := range pair.Runways {
			if _, ok := LookupRunway(icao, rwy); !ok {
				e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[icao].ValidRunways())
			}
		}

		// Find the runway intersection point
		reg0, reg1 := ap.ApproachRegions[pair.Runways[0]], ap.ApproachRegions[pair.Runways[1]]
		if reg0 != nil && reg1 != nil {
			// If either is nil, we'll flag the error below, so it's fine to ignore that here.
			r0n := reg0.NearPoint(nmPerLongitude, magneticVariation)
			r0f := reg0.FarPoint(nmPerLongitude, magneticVariation)
			r1n := reg1.NearPoint(nmPerLongitude, magneticVariation)
			r1f := reg1.FarPoint(nmPerLongitude, magneticVariation)

			p, ok := math.LineLineIntersect(r0n, r0f, r1n, r1f)
			if ok && math.Distance2f(p, r0n) < 10 && math.Distance2f(p, r1n) < 10 {
				ap.ConvergingRunways[i].RunwayIntersection = math.NM2LL(p, nmPerLongitude)
			} else {
				mid := math.Scale2f(math.Add2f(math.LL2NM(reg0.ReferencePoint, nmPerLongitude),
					math.LL2NM(reg1.ReferencePoint, nmPerLongitude)),
					0.5)

				ap.ConvergingRunways[i].RunwayIntersection = math.NM2LL(mid, nmPerLongitude)
			}
		}

		for j, rwy := range pair.Runways {
			e.Push(rwy)
			var err error
			ap.ConvergingRunways[i].LeaderDirections[j], err =
				math.ParseCardinalOrdinalDirection(pair.LeaderDirectionStrings[j])
			if err != nil {
				e.Error(err)
			}

			if _, ok := ap.ApproachRegions[rwy]; !ok {
				e.ErrorString("runway not defined in \"approach_regions\"")
			}
			e.Pop()
		}
		e.Pop()
	}

	// Generate reasonable default ATPA volumes for any runways they aren't
	// specified for.
	if ap.ATPAVolumes == nil {
		ap.ATPAVolumes = make(map[string]*ATPAVolume)
	}
	for _, rwy := range DB.Airports[icao].Runways {
		if _, ok := ap.ATPAVolumes[rwy.Id]; !ok {
			// Make a default volume
			ap.ATPAVolumes[rwy.Id] = &ATPAVolume{
				Id:        rwy.Id,
				Threshold: rwy.Threshold,
				Heading:   rwy.Heading,
			}
		}
	}

	for rwy, vol := range ap.ATPAVolumes {
		e.Push("ATPA " + rwy)

		if vol.Id == "" {
			vol.Id = rwy
		}

		if _, ok := LookupRunway(icao, rwy); !ok {
			e.ErrorString("runway %q is unknown. Options: %s", rwy, DB.Airports[icao].ValidRunways())
		}

		if vol.Threshold.IsZero() { // the location is set directly for default volumes
			if vol.ThresholdString == "" {
				e.ErrorString("\"runway_threshold\" not specified.")
			} else {
				var ok bool
				if vol.Threshold, ok = loc.Locate(vol.ThresholdString); !ok {
					e.ErrorString("%q unknown for \"runway_threshold\".", vol.ThresholdString)
				}
			}
		}

		// Defaults if things are not specified
		if vol.MaxHeadingDeviation == 0 {
			vol.MaxHeadingDeviation = 90
		}
		if vol.Floor == 0 {
			vol.Floor = float32(DB.Airports[icao].Elevation + 100)
		}
		if vol.Ceiling == 0 {
			vol.Ceiling = float32(DB.Airports[icao].Elevation + 5000)
		}
		if vol.Length == 0 {
			vol.Length = 15
		}
		if vol.LeftWidth == 0 {
			vol.LeftWidth = 2000
		}
		if vol.RightWidth == 0 {
			vol.RightWidth = 2000
		}

		e.Pop()
	}
}

func (ap Airport) VFRRateSum() int {
	sum := ap.VFR.Randoms.Rate
	for _, spec := range ap.VFR.Routes {
		sum += spec.Rate
	}
	return sum
}

type ExitRoute struct {
	SID              string        `json:"sid"`
	AssignedAltitude int           `json:"assigned_altitude"`
	ClearedAltitude  int           `json:"cleared_altitude"`
	SpeedRestriction int           `json:"speed_restriction"`
	Waypoints        WaypointArray `json:"waypoints"`
	Description      string        `json:"description"`
	// optional, control position to handoff to at a /ho
	HandoffController string `json:"handoff_controller"`
}

type Departure struct {
	Exit string `json:"exit"`

	Destination         string                  `json:"destination"`
	Altitudes           util.SingleOrArray[int] `json:"altitude,omitempty"`
	Route               string                  `json:"route"`
	RouteWaypoints      WaypointArray           // not specified in user JSON
	Airlines            []DepartureAirline      `json:"airlines"`
	Scratchpad          string                  `json:"scratchpad"`           // optional
	SecondaryScratchpad string                  `json:"secondary_scratchpad"` // optional
	Description         string                  `json:"description"`
}

type DepartureAirline struct {
	AirlineSpecifier
}

type ApproachType int

const (
	ILSApproach ApproachType = iota
	RNAVApproach
	ChartedVisualApproach
	LocalizerApproach
	VORApproach
)

func (at ApproachType) String() string {
	return []string{"ILS", "RNAV", "Charted Visual", "Localizer", "VOR"}[at]
}

func (at ApproachType) MarshalJSON() ([]byte, error) {
	switch at {
	case ILSApproach:
		return []byte("\"ILS\""), nil
	case RNAVApproach:
		return []byte("\"RNAV\""), nil
	case ChartedVisualApproach:
		return []byte("\"Visual\""), nil
	case LocalizerApproach:
		return []byte("\"Localizer\""), nil
	case VORApproach:
		return []byte("\"VOR\""), nil
	default:
		return nil, fmt.Errorf("unhandled approach type in MarshalJSON()")
	}
}

func (at *ApproachType) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "\"ILS\"":
		*at = ILSApproach
		return nil

	case "\"RNAV\"":
		*at = RNAVApproach
		return nil

	case "\"Visual\"":
		*at = ChartedVisualApproach
		return nil

	case "\"Localizer\"":
		*at = LocalizerApproach
		return nil

	case "\"VOR\"":
		*at = VORApproach
		return nil

	default:
		return fmt.Errorf("%s: unknown approach_type", string(b))
	}
}

type Approach struct {
	Id        string          `json:"cifp_id"`
	FullName  string          `json:"full_name"`
	Type      ApproachType    `json:"type"`
	Runway    string          `json:"runway"`
	Waypoints []WaypointArray `json:"waypoints"`
	// Note: this isn't currently documented; currently it's only set when
	// we have a canonical value from the CIFP.
	ApproachHeading float32 `json:"approach_heading"`
}

// Find the FAF: return the corresponding waypoint array and the index of the FAF within it.
func (ap *Approach) FAFSegment(nmPerLongitude, magneticVariation float32) ([]Waypoint, int) {
	// For approaches with multiple segments, want the segment that is most
	// closely aligned with the runway. For CIFP routes, we could grab
	// 5.26, outbound magnetic course, but we don't have that for
	// user-specified routes. So we'll work out the approximate runway
	// heading from the runway string and match that one.
	rwy, _ := strconv.Atoi(strings.TrimRight(ap.Runway, "LRC")) // Not sure what can be done for error handling here...
	rwy *= 10

	bestWpsIdx, bestWpsFAFIdx := -1, -1
	minDiff := float32(360)

	for i, wps := range ap.Waypoints {
		fafIdx := slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.FAF })
		if fafIdx == -1 {
			// no FAF on this segment(?)
			continue
		}

		if wps[fafIdx].IF || wps[fafIdx].IAF {
			// Likely a HILPT; don't go outbound for the approach course as
			// it may be some random feeder fix.
			fafIdx++
		}

		// Go from the previous fix to the FAF if possible.
		if fafIdx == 0 {
			fafIdx++
		}

		hdg := math.Heading2LL(wps[fafIdx-1].Location, wps[fafIdx].Location, nmPerLongitude, magneticVariation)

		diff := math.HeadingDifference(hdg, float32(rwy))
		if diff < minDiff {
			minDiff = diff
			bestWpsIdx = i
			bestWpsFAFIdx = fafIdx
		}
	}

	if bestWpsIdx != -1 {
		return ap.Waypoints[bestWpsIdx], bestWpsFAFIdx
	} else {
		// Shouldn't ever happen since we ensure there is a FAF for each approach.
		return nil, 0
	}
}

func (ap *Approach) Line(nmPerLongitude, magneticVariation float32) [2]math.Point2LL {
	if ap.ApproachHeading != 0 {
		for _, wps := range ap.Waypoints {
			if idx := slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.FAF }); idx != -1 {
				// Found the FAF
				return [2]math.Point2LL{wps[idx].Location,
					math.Offset2LL(wps[idx].Location, ap.ApproachHeading, 1, nmPerLongitude, magneticVariation)}
			}
		}
	}

	wps, idx := ap.FAFSegment(nmPerLongitude, magneticVariation)
	return [2]math.Point2LL{wps[idx-1].Location, wps[idx].Location}
}

func (ap *Approach) Heading(nmPerLongitude, magneticVariation float32) float32 {
	if ap.ApproachHeading != 0 {
		return ap.ApproachHeading
	}

	p := ap.Line(nmPerLongitude, magneticVariation)
	return math.Heading2LL(p[0], p[1], nmPerLongitude, magneticVariation)
}
