package sim

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

// TODO:
// idt
// receivedmessages
// adaptation
// starscomputers
// eraminboxes
// trackinfo
// stars:
// receivesmessages
// idt
// eraminbox
// unsupported
// starsinboxes
// trackinfo

// Message types sent from either ERAM or STARS
const (
	Plan              = iota // Both STARS & ERAM send this.
	Amendment                // ERAM (STARS?)
	Cancellation             // ERAM (STARS?)
	RequestFlightPlan        // STARS
	DepartureDM              // STARS
	BeaconTerminate          // STARS

	// Track Data

	InitiateTransfer     // When handoff gets sent. Sends the flightplan, contains track location
	AcceptRecallTransfer // Accept/ recall handoff
	// updated track coordinates. If off by some amount that is unaccepable, you'd see "AMB" in STARS datatag.
	// If no target is even close with same beacon code on the receiving STARS system, you'd see "NAT".

	// TODO:
	// Track Data
	// Test
	// Response
)

type ERAMComputer struct {
	STARSComputers   map[string]*STARSComputer
	ERAMInboxes      map[string]*[]FlightPlanMessage
	ReceivedMessages *[]FlightPlanMessage
	FlightPlans      map[av.Squawk]*STARSFlightPlan
	TrackInformation map[string]*TrackInformation
	AvailableSquawks map[av.Squawk]interface{}
	Identifier       string
	Adaptation       av.ERAMAdaptation

	lg *log.Logger
}

func MakeERAMComputer(fac string, starsBeaconBank int, lg *log.Logger) (*ERAMComputer, error) {
	ec := &ERAMComputer{
		STARSComputers:   make(map[string]*STARSComputer),
		ERAMInboxes:      make(map[string]*[]FlightPlanMessage),
		ReceivedMessages: &[]FlightPlanMessage{},
		FlightPlans:      make(map[av.Squawk]*STARSFlightPlan),
		TrackInformation: make(map[string]*TrackInformation),
		AvailableSquawks: getValidSquawkCodes(),
		Identifier:       fac,
		lg:               lg,
	}

	var ok bool
	if ec.Adaptation, ok = av.DB.ERAMAdaptations[fac]; !ok {
		return nil, ErrUnknownFacility
	}

	starsAvailableSquawks := getBeaconBankSquawks(starsBeaconBank)

	for id, tracon := range av.DB.TRACONs {
		if tracon.ARTCC == fac {
			sc := MakeSTARSComputer(id, starsAvailableSquawks)
			// make the ERAM inbox
			sc.ERAMInbox = ec.ReceivedMessages
			ec.STARSComputers[id] = sc
		}
	}

	return ec, nil
}

func getValidSquawkCodes() map[av.Squawk]interface{} {
	sq := make(map[av.Squawk]interface{})

	for i := 0o1001; i <= 0o7777; i++ {
		// Skip SPCs and VFR
		if spc, _ := av.SquawkIsSPC(av.Squawk(i)); !spc && i != 0o1200 {
			sq[av.Squawk(i)] = nil
		}
	}
	return sq
}

func getBeaconBankSquawks(bank int) map[av.Squawk]interface{} {
	sq := make(map[av.Squawk]interface{})

	for i := bank*0o100 + 1; i <= bank*0o100+0o77; i++ {
		sq[av.Squawk(i)] = nil
	}
	return sq
}

// For NAS codes
func (comp *ERAMComputer) CreateSquawk() (av.Squawk, error) {
	// Pick an available one at random
	for sq := range comp.AvailableSquawks {
		delete(comp.AvailableSquawks, sq)
		return sq, nil
	}
	return av.Squawk(0), ErrNoMoreAvailableSquawkCodes
}

func (comp *ERAMComputer) SendFlightPlans(tracon string, simTime time.Time) {
	sendPlanIfReady := func(fp *STARSFlightPlan) {
		if simTime.Add(TransmitFPMessageTime).Before(fp.CoordinationTime.Time) {
			return
		}

		if coordFix, ok := comp.Adaptation.CoordinationFixes[fp.CoordinationFix]; !ok {
			comp.lg.Errorf("%s: no coordination fix found for STARSFlightPlan CoordinationFix",
				fp.CoordinationFix)
		} else if adaptFix, err := coordFix.Fix(fp.Altitude); err != nil {
			comp.lg.Errorf("%s @ %s", fp.CoordinationFix, fp.Altitude)
		} else if !slices.Contains(fp.ContainedFacilities, adaptFix.ToFacility) {
			comp.SendFlightPlan(fp, tracon, simTime)
		}
	}

	for _, info := range comp.TrackInformation {
		if fp := info.FlightPlan; fp != nil {
			if fp.Callsign == "" && fp.Altitude == "" {
				// FIXME(mtrokel): figure out why these are sneaking in here!
				delete(comp.TrackInformation, info.Identifier)
			} else {
				sendPlanIfReady(fp)
			}
		}
	}
	for _, fp := range comp.FlightPlans {
		sendPlanIfReady(fp)
	}
}

// For individual plans being sent.
func (comp *ERAMComputer) SendFlightPlan(fp *STARSFlightPlan, tracon string, simTime time.Time) error {
	msg := fp.Message()
	msg.MessageType = Plan
	msg.SourceID = formatSourceID(comp.Identifier, simTime)

	if coordFix, ok := comp.Adaptation.CoordinationFixes[fp.CoordinationFix]; !ok {
		return av.ErrNoMatchingFix
	} else if adaptFix, err := coordFix.Fix(fp.Altitude); err != nil {
		return err
	} else {
		// TODO: change tracon to the fix pair assignment (this will be in the adaptation)
		err := comp.ToSTARSFacility(tracon, msg)
		if err != nil {
			comp.SendMessageToERAM(av.DB.TRACONs[tracon].ARTCC, msg)
		}
		fp.ContainedFacilities = append(fp.ContainedFacilities, adaptFix.ToFacility)
		return nil
	}
}

// Sends a message, whether that be a flight plan or any other message type to a STARS computer.
// The STARS computer will sort messages by itself
func (comp *ERAMComputer) ToSTARSFacility(facility string, msg FlightPlanMessage) error {
	if stars, ok := comp.STARSComputers[facility]; !ok {
		return ErrUnknownFacility
	} else {
		stars.ReceivedMessages = append(stars.ReceivedMessages, msg)
		return nil
	}
}

func (comp *ERAMComputer) SendMessageToERAM(facility string, msg FlightPlanMessage) error {
	if inbox, ok := comp.ERAMInboxes[facility]; !ok {
		return ErrUnknownFacility
	} else {
		*inbox = append(*inbox, msg)
		return nil

	}
}

func (comp *ERAMComputer) SortMessages(simTime time.Time) {
	for _, msg := range *comp.ReceivedMessages {
		switch msg.MessageType {
		case Plan:
			fp := msg.FlightPlan()

			if fp.AssignedSquawk == av.Squawk(0) {
				// TODO: Figure out why it's sending a blank fp
				panic("zero squawk")
			}

			// Ensure comp.FlightPlans[msg.BCN] is initialized
			comp.FlightPlans[msg.BCN] = fp

			if fp.CoordinationFix == "" {
				var ok bool
				fp.CoordinationFix, ok = comp.FixForRouteAndAltitude(fp.Route, fp.Altitude)
				if !ok {
					comp.lg.Warnf("Coordination fix not found for route \"%s\", altitude \"%s",
						fp.Route, fp.Altitude)
					continue
				}
			}

			// Check if another facility needs this plan.
			if af, ok := comp.AdaptationFixForAltitude(fp.CoordinationFix, fp.Altitude); ok {
				if af.ToFacility != comp.Identifier {
					// Send the plan to the STARS facility that needs it.
					comp.ToSTARSFacility(af.ToFacility, msg)
				}
			}

		case RequestFlightPlan:
			facility := msg.SourceID[:3] // Facility asking for FP
			// Find the flight plan
			plan, ok := comp.FlightPlans[msg.BCN]
			if ok {
				msg := FlightPlanDepartureMessage(plan.FlightPlan, comp.Identifier, simTime)
				comp.ToSTARSFacility(facility, msg)
			}

			// FIXME: why is this here?
			*comp.ReceivedMessages = (*comp.ReceivedMessages)[1:]

		case DepartureDM: // Stars ERAM coordination time tracking

		case BeaconTerminate: // TODO: Find out what this does

		case InitiateTransfer:
			// Forward these to w.TRACON for now. ERAM adaptations will have to fix this eventually...

			if comp.TrackInformation[msg.Identifier] == nil {
				comp.TrackInformation[msg.Identifier] = &TrackInformation{
					FlightPlan: comp.FlightPlans[msg.BCN],
				}
			}
			comp.TrackInformation[msg.Identifier].TrackOwner = msg.TrackOwner
			comp.TrackInformation[msg.Identifier].HandoffController = msg.HandoffController
			comp.AvailableSquawks[msg.BCN] = nil

			for name, fixes := range comp.Adaptation.CoordinationFixes {
				alt := comp.TrackInformation[msg.Identifier].FlightPlan.Altitude
				if fix, err := fixes.Fix(alt); err != nil {
					comp.lg.Warnf("Couldn't find adaptation fix: %v. Altitude \"%s\", Fixes %+v",
						err, alt, fixes)
				} else {
					if name == msg.CoordinationFix && fix.ToFacility != comp.Identifier { // Forward
						msg.SourceID = formatSourceID(comp.Identifier, simTime)
						if to := fix.ToFacility; len(to) > 0 && to[0] == 'Z' { // To another ARTCC
							comp.SendMessageToERAM(to, msg)
						} else { // To a TRACON
							comp.ToSTARSFacility(to, msg)
						}
					} else if name == msg.CoordinationFix && fix.ToFacility == comp.Identifier { // Stay here
						comp.TrackInformation[msg.Identifier] = &TrackInformation{
							TrackOwner:        msg.TrackOwner,
							HandoffController: msg.HandoffController,
							FlightPlan:        comp.FlightPlans[msg.BCN],
						}
					}
				}
			}

		case AcceptRecallTransfer:
			adaptationFixes, ok := comp.Adaptation.CoordinationFixes[msg.CoordinationFix]
			if !ok {
				comp.lg.Warnf("%s: adaptation fixes not found for coordination fix",
					msg.CoordinationFix)
			} else {
				if info := comp.TrackInformation[msg.Identifier]; info != nil {
					// Recall message, we can free up this code now
					if msg.TrackOwner == info.TrackOwner {
						comp.AvailableSquawks[msg.BCN] = nil
					}
					info.TrackOwner = msg.TrackOwner
				}

				altitude := comp.TrackInformation[msg.Identifier].FlightPlan.Altitude
				if adaptationFix, err := adaptationFixes.Fix(altitude); err == nil {
					if adaptationFix.FromFacility != comp.Identifier {
						// Comes from a different ERAM facility
						comp.SendMessageToERAM(adaptationFix.FromFacility, msg)
					}
				}
			}
		}
	}

	clear(*comp.ReceivedMessages)
}

func (ec *ERAMComputer) FixForRouteAndAltitude(route string, altitude string) (string, bool) {
	return ec.Adaptation.FixForRouteAndAltitude(route, altitude)
}

func (ec *ERAMComputer) AdaptationFixForAltitude(fix string, altitude string) (av.AdaptationFix, bool) {
	return ec.Adaptation.AdaptationFixForAltitude(fix, altitude)
}

type ERAMComputers map[string]*ERAMComputer

type ERAMTrackInfo struct {
	Location          math.Point2LL
	Owner             string
	HandoffController string
}

const TransmitFPMessageTime = 30 * time.Minute

type STARSComputer struct {
	Identifier        string
	ContainedPlans    map[av.Squawk]*STARSFlightPlan
	ReceivedMessages  []FlightPlanMessage
	TrackInformation  map[string]*TrackInformation
	ERAMInbox         *[]FlightPlanMessage            // The address of the overlying ERAM's message inbox.
	STARSInbox        map[string]*[]FlightPlanMessage // Other STARS Facilities' inboxes
	UnsupportedTracks map[int]*UnsupportedTrack
	AvailableSquawks  map[av.Squawk]interface{}
}

func MakeSTARSComputer(id string, sq map[av.Squawk]interface{}) *STARSComputer {
	return &STARSComputer{
		Identifier:        id,
		ContainedPlans:    make(map[av.Squawk]*STARSFlightPlan),
		TrackInformation:  make(map[string]*TrackInformation),
		STARSInbox:        make(map[string]*[]FlightPlanMessage),
		UnsupportedTracks: make(map[int]*UnsupportedTrack), // Using one value for the bank is good enough (for now)
		AvailableSquawks:  sq,
	}
}

// For local codes
func (comp *STARSComputer) CreateSquawk() (av.Squawk, error) {
	for sq := range comp.AvailableSquawks {
		delete(comp.AvailableSquawks, sq)
		return sq, nil
	}
	return av.Squawk(0), ErrNoMoreAvailableSquawkCodes
}

func (comp *STARSComputer) SendTrackInfo(receivingFacility string, msg FlightPlanMessage, simTime time.Time) {
	msg.SourceID = formatSourceID(comp.Identifier, simTime)
	if inbox := comp.STARSInbox[receivingFacility]; inbox != nil {
		*inbox = append(*inbox, msg)
	} else {
		comp.SendToOverlyingERAMFacility(msg)
	}
}

func formatSourceID(id string, t time.Time) string {
	return id + t.Format("1504Z")
}

func (comp *STARSComputer) SendToOverlyingERAMFacility(msg FlightPlanMessage) {
	*comp.ERAMInbox = append(*comp.ERAMInbox, msg)
}

func (comp *STARSComputer) RequestFlightPlan(bcn av.Squawk, simTime time.Time) {
	message := FlightPlanMessage{
		MessageType: RequestFlightPlan,
		BCN:         bcn,
		SourceID:    formatSourceID(comp.Identifier, simTime),
	}
	comp.SendToOverlyingERAMFacility(message)
}

// Sorting the STARS messages. This will store flight plans with FP
// messages, change flight plans with AM messages, cancel flight plans with
// CX messages, etc.
func (comp *STARSComputer) SortReceivedMessages(e *EventStream) {
	for _, msg := range comp.ReceivedMessages {
		switch msg.MessageType {
		case Plan:
			if msg.BCN != av.Squawk(0) {
				comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			}

		case Amendment:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()

		case Cancellation: // Deletes the flight plan from the computer
			delete(comp.ContainedPlans, msg.BCN)

		case InitiateTransfer:
			// 1. Store the data comp.trackinfo. We now know who's tracking
			// the plane. Use the squawk to get the plan.
			if fp := comp.ContainedPlans[msg.BCN]; fp != nil { // We have the plan
				comp.TrackInformation[msg.Identifier] = &TrackInformation{
					TrackOwner:        msg.TrackOwner,
					HandoffController: msg.HandoffController,
					FlightPlan:        fp,
				}

				delete(comp.ContainedPlans, msg.BCN)

				e.Post(Event{
					Type:         TransferAcceptedEvent,
					Callsign:     msg.Identifier,
					ToController: msg.TrackOwner,
				})
			} else {
				if trk := comp.TrackInformation[msg.Identifier]; trk != nil {
					comp.TrackInformation[msg.Identifier] = &TrackInformation{
						TrackOwner:        msg.TrackOwner,
						HandoffController: msg.HandoffController,
						FlightPlan:        trk.FlightPlan,
					}

					delete(comp.ContainedPlans, msg.BCN)

					e.Post(Event{
						Type:         TransferAcceptedEvent,
						Callsign:     msg.Identifier,
						ToController: msg.TrackOwner,
					})
				} else { // send an IF msg
					e.Post(Event{
						Type:         TransferRejectedEvent,
						Callsign:     msg.Identifier,
						ToController: msg.TrackOwner,
					})
				}

			}

		case AcceptRecallTransfer:
			// - When we send an accept message, we set the track ownership to us.
			// - When we receive an accept message, we change the track
			//   ownership to the receiving controller.
			// - When we send a recall message, we tell our system to stop the flashing.
			// - When we receive a recall message, we keep the plan and if
			//   we click the track, it is no longer able to be accepted
			//
			// We can infer whether its a recall/ accept by the track ownership that gets sent back.
			info := comp.TrackInformation[msg.Identifier]
			if info == nil {
				break
			}

			if msg.TrackOwner != info.TrackOwner {
				// It has to be an accept message. (We initiated the handoff here)
				info.TrackOwner = msg.TrackOwner
				info.HandoffController = ""
			} else {
				// It has to be a recall message. (we received the handoff)
				delete(comp.TrackInformation, msg.Identifier)
			}
		}
	}

	clear(comp.ReceivedMessages)
}

type STARSFlightPlan struct {
	av.FlightPlan
	FlightPlanType      int
	CoordinationTime    CoordinationTime
	CoordinationFix     string
	ContainedFacilities []string
	Altitude            string
	SP1                 string
	SP2                 string
	InitialController   string // For abbreviated FPs
}

// Flight plan types (STARS)
const (
	// Flight plan received from a NAS ARTCC.  This is a flight plan that
	// has been sent over by an overlying ERAM facility.
	RemoteEnroute = iota

	// Flight plan received from an adjacent terminal facility This is a
	// flight plan that has been sent over by another STARS facility.
	RemoteNonEnroute

	// VFR interfacility flight plan entered locally for which the NAS
	// ARTCC has not returned a flight plan This is a flight plan that is
	// made by a STARS facility that gets a NAS code.
	LocalEnroute

	// Flight plan entered by TCW or flight plan from an adjacent terminal
	// that has been handed off to this STARS facility This is a flight
	// plan that is made at a STARS facility and gets a local code.
	LocalNonEnroute
)

func (fp STARSFlightPlan) Message() FlightPlanMessage {
	return FlightPlanMessage{
		BCN:      fp.AssignedSquawk,
		Altitude: fp.Altitude, // Eventually we'll change this to a string
		Route:    fp.Route,
		AircraftData: AircraftDataMessage{
			DepartureLocation: fp.DepartureAirport,
			ArrivalLocation:   fp.ArrivalAirport,
			NumberOfAircraft:  1,
			AircraftType:      fp.TypeWithoutSuffix(),
			AircraftCategory:  fp.AircraftType, // TODO: Use a method to turn this into an aircraft category
			Equipment:         strings.TrimPrefix(fp.AircraftType, fp.TypeWithoutSuffix()),
		},
		FlightID:         fp.ECID + fp.Callsign,
		CoordinationFix:  fp.CoordinationFix,
		CoordinationTime: fp.CoordinationTime,
	}
}

type FlightPlanMessage struct {
	SourceID         string // LLLdddd e.g. ZCN2034 (ZNY at 2034z)
	MessageType      int
	FlightID         string // ddaLa(a)(a)(a)(a)(a)ECID (3 chars start w/ digit), Aircraft ID (2-7 chars start with letter)
	AircraftData     AircraftDataMessage
	BCN              av.Squawk
	CoordinationFix  string
	CoordinationTime CoordinationTime

	// Altitude will either be requested (cruise altitude) for departures,
	// or the assigned altitude for arrivals.  ERAM has the ability to
	// assign interm alts (and is used much more than STARS interm alts)
	// with `QQ`.  This interim altiude gets sent down to the STARS
	// computer instead of the cruising altitude. If no interim altitude is
	// set, use the cruise altitude (check this) Examples of altitudes
	// could be 310, VFR/170, VFR, 170B210 (block altitude), etc.
	Altitude string
	Route    string

	TrackInformation // For track messages
}

type TrackInformation struct {
	Identifier        string
	TrackOwner        string
	HandoffController string
	FlightPlan        *STARSFlightPlan
	PointOut          string
	PointOutHistory   []string
	RedirectedHandoff av.RedirectedHandoff
	SP1               string
	SP2               string
	AutoAssociateFP   bool // If it's white or not
}

const (
	DepartureTime  = "P"
	ArrivalTime    = "A"
	OverflightTime = "E"
)

type CoordinationTime struct {
	Time time.Time
	Type string // A for arrivals, P for Departures, E for overflights
}

type AircraftDataMessage struct {
	DepartureLocation string // Only for departures.
	ArrivalLocation   string // Only for arrivals. I think this is made up, but I don't know where to get the arrival info from.
	NumberOfAircraft  int    // Default this at one for now.
	AircraftType      string // A20N, B737, etc.

	// V = VFR (not heavy jet),
	// H = Heavy,
	// W = Heavy + VFR,
	// U = Heavy + OTP.
	AircraftCategory string
	Equipment        string // /L, /G, /A, etc
}

const (
	ACID = iota
	BCN
	ControllingPosition
	TypeOfFlight // Figure out this
	SC1
	SC2
	AircraftType
	RequestedALT
	Rules
	DepartureAirport // Specified with type of flight (maybe)
	Errors
)

type AbbreviatedFPFields struct {
	ACID                string
	BCN                 av.Squawk
	ControllingPosition string
	TypeOfFlight        string // Figure out this
	SC1                 string
	SC2                 string
	AircraftType        string
	RequestedALT        string
	Rules               av.FlightRules
	DepartureAirport    string // Specified with type of flight (maybe)
	Error               error
}

type UnsupportedTrack struct {
	TrackLocation     math.Point2LL
	Owner             string
	HandoffController string
	FlightPlan        *STARSFlightPlan
}

// starsBeaconBank -> w.STARSFacilityAdaptation.BeaconBank
func MakeERAMComputers(starsBeaconBank int, lg *log.Logger) (ERAMComputers, error) {
	ec := make(map[string]*ERAMComputer)

	// Make the ERAM computer for each ARTCC
	for fac := range av.DB.ARTCCs {
		var err error
		if ec[fac], err = MakeERAMComputer(fac, starsBeaconBank, lg); err != nil {
			return nil, err
		}
	}

	// Let each ERAM computer know about the other ARTCC ERAM computers'
	// inboxes.
	//
	// TODO: remove this, just look it up from ERAMComputers when we need
	// it.
	for fac, comp := range ec {
		for fac2, comp2 := range ec {
			// Don't add our own ERAM to the inbox.
			if fac != fac2 {
				comp.ERAMInboxes[fac2] = comp2.ReceivedMessages
			}
		}
	}

	allSTARSInboxes := make(map[string]*[]FlightPlanMessage)
	for _, eram := range ec {
		for _, stars := range eram.STARSComputers {
			allSTARSInboxes[stars.Identifier] = &stars.ReceivedMessages
		}
	}

	// Initialize STARSInbox in the STARSComputers; we store a pointer to
	// all other STARSComputers' inboxes in each STARSComputer.
	//
	// TODO: this also should probably be removed, to be looked up when
	// needed.
	for _, eram := range ec {
		for _, stars := range eram.STARSComputers {
			for tracon, address := range allSTARSInboxes {
				if tracon != stars.Identifier {
					stars.STARSInbox[tracon] = address
				}
			}
		}
	}

	return ERAMComputers(ec), nil
}

// If given an ARTCC, returns the corresponding ERAMComputer; if given a TRACON,
// returns both the associated ERMANComputer and STARSComputer
func (ec *ERAMComputers) FacilityComputers(fac string) (*ERAMComputer, *STARSComputer, error) {
	if ec, ok := (*ec)[fac]; ok {
		// fac is an ARTCC
		return ec, nil, nil
	}

	tracon, ok := av.DB.TRACONs[fac]
	if !ok {
		return nil, nil, ErrUnknownFacility
	}

	eram, ok := (*ec)[tracon.ARTCC]
	if !ok {
		// This shouldn't happen...
		panic("no ERAM computer found for " + tracon.ARTCC + " from TRACON " + fac)
	}

	stars, ok := eram.STARSComputers[fac]
	if !ok {
		// This also shouldn't happen...
		panic("no STARS computer found for " + fac)
	}

	return eram, stars, nil
}

// Give the computers a chance to sort through their received
// messages. Messages will send when the time is appropriate (e.g.,
// handoff).  Some messages will be sent from recieved messages (for
// example a FP message from a RF message).
func (ec ERAMComputers) UpdateComputers(tracon string, simTime time.Time, e *EventStream) {
	// _, fac := w.FacilityComputers(FIXME)
	// Sort through messages made
	for _, comp := range ec {
		comp.SortMessages(simTime)
		comp.SendFlightPlans(tracon, simTime)
		for _, stars := range comp.STARSComputers {
			stars.SortReceivedMessages(e)
		}
	}
}

// For debugging purposes
func (e ERAMComputers) DumpMap() {
	for key, eramComputer := range e {
		allowedFacilities := []string{"ZNY", "ZDC", "ZBW"} // Just so the console doesn't get flodded with empty ARTCCs (I debug with EWR)
		if !slices.Contains(allowedFacilities, key) {
			continue
		}
		fmt.Printf("Key: %s\n", key)
		fmt.Printf("Identifier: %s\n", eramComputer.Identifier)

		fmt.Println("STARSComputers:")
		for scKey, starsComputer := range eramComputer.STARSComputers {
			fmt.Printf("\tKey: %s, Identifier: %s\n", scKey, starsComputer.Identifier)
			fmt.Printf("\tReceivedMessages: %v\n\n", starsComputer.ReceivedMessages)

			fmt.Println("\tContainedPlans:")
			for sq, plan := range starsComputer.ContainedPlans {
				fmt.Printf("\t\tSquawk: %s, Callsign %v, Plan: %+v\n\n", sq, plan.Callsign, *plan)
			}

			fmt.Println("\tTrackInformation:")
			for sq, trackInfo := range starsComputer.TrackInformation {
				fmt.Printf("\tIdentifier: %s, TrackInfo:\n", sq)
				fmt.Printf("\t\tIdentifier: %+v\n", trackInfo.Identifier)
				fmt.Printf("\t\tOwner: %s\n", trackInfo.TrackOwner)
				fmt.Printf("\t\tHandoffController: %s\n", trackInfo.HandoffController)
				if trackInfo.FlightPlan != nil {
					fmt.Printf("\t\tFlightPlan: %+v\n\n", *trackInfo.FlightPlan)
				} else {
					fmt.Printf("\t\tFlightPlan: nil\n\n")
				}
			}

			if starsComputer.ERAMInbox != nil {
				fmt.Printf("\tERAMInbox: %v\n", *starsComputer.ERAMInbox)
			}

		}

		fmt.Println("ERAMInboxes:")
		for eiKey, inbox := range eramComputer.ERAMInboxes {
			fmt.Printf("\tKey: %s, Messages: %v\n\n", eiKey, *inbox)
		}

		if eramComputer.ReceivedMessages != nil {
			fmt.Printf("ReceivedMessages: %v\n\n", *eramComputer.ReceivedMessages)
		}

		fmt.Println("FlightPlans:")
		for sq, plan := range eramComputer.FlightPlans {
			fmt.Printf("\tSquawk: %s, Plan: %+v\n\n", sq, *plan)
		}

		fmt.Println("TrackInformation:")
		for sq, trackInfo := range eramComputer.TrackInformation {
			fmt.Printf("\tIdentifier: %s, TrackInfo:\n", sq)
			fmt.Printf("\t\tIdentifier: %+v\n", trackInfo.Identifier)
			fmt.Printf("\t\tOwner: %s\n", trackInfo.TrackOwner)
			fmt.Printf("\t\tHandoffController: %s\n", trackInfo.HandoffController)
			if trackInfo.FlightPlan != nil {
				fmt.Printf("\t\tFlightPlan: %+v\n\n", *trackInfo.FlightPlan)
			} else {
				fmt.Printf("\t\tFlightPlan: nil\n\n")
			}

		}
	}
}

// Converts the message to a STARS flight plan.
func (s FlightPlanMessage) FlightPlan() *STARSFlightPlan {
	rules := av.FlightRules(util.Select(strings.Contains(s.Altitude, "VFR"), av.VFR, av.IFR))
	flightPlan := &STARSFlightPlan{
		FlightPlan: av.FlightPlan{
			Rules:            rules,
			AircraftType:     s.AircraftData.AircraftType,
			AssignedSquawk:   s.BCN,
			DepartureAirport: s.AircraftData.DepartureLocation,
			ArrivalAirport:   s.AircraftData.ArrivalLocation,
			Route:            s.Route,
		},
		CoordinationFix:  s.CoordinationFix,
		CoordinationTime: s.CoordinationTime,
		Altitude:         s.Altitude,
	}

	if len(s.FlightID) > 3 {
		flightPlan.ECID = s.FlightID[:3]
		flightPlan.Callsign = s.FlightID[3:]
	}

	return flightPlan
}

// Prepare the message to sent to a STARS facility after a RF message
func FlightPlanDepartureMessage(fp av.FlightPlan, sendingFacility string, simTime time.Time) FlightPlanMessage {
	return FlightPlanMessage{
		SourceID:    formatSourceID(sendingFacility, simTime),
		MessageType: Plan,
		FlightID:    fp.ECID + fp.Callsign,
		AircraftData: AircraftDataMessage{
			DepartureLocation: fp.DepartureAirport,
			ArrivalLocation:   fp.ArrivalAirport,
			NumberOfAircraft:  1, // One for now.
			AircraftType:      fp.TypeWithoutSuffix(),
			AircraftCategory:  fp.AircraftType, // TODO: Use a method to turn this into an aircraft category
			Equipment:         strings.TrimPrefix(fp.AircraftType, fp.TypeWithoutSuffix()),
		},
		BCN:             fp.AssignedSquawk,
		CoordinationFix: fp.Exit,
		Altitude:        util.Select(fp.Rules == av.VFR, "VFR/", "") + strconv.Itoa(fp.Altitude),
		Route:           fp.Route,
	}
}

/*

func (w *World) FacilityFromController(callsign string) string {
	controller := w.GetControllerByCallsign(callsign)
	if controller != nil && controller.Facility != "" {
		return controller.Facility
	} else if controller != nil {
		return w.TRACON
	}
	if len(callsign) == 7 && (callsign[3:] == "_APP" || callsign[3:] == "_DEP") {
		return w.TRACON // figure out why sometimes EWR_APP (primary controller) doesn't show up
	}
	return ""
}

func (fp *STARSFlightPlan) GetCoordinationFix(w *World, ac *Aircraft) string {
	fixes := w.STARSFacilityAdaptation.CoordinationFixes
	for fix, multiple := range fixes {

		info := multiple.Fix(fp.Altitude)

		if info.Type == ZoneBasedFix { // Exclude zone based fixes for now. They come in after the route-based fix
			continue
		}
		if strings.Contains(fp.Route, fix) {
			return fix
		}
		for _, waypoint := range ac.Nav.Waypoints {
			if waypoint.Fix == fix {
				return fix
			}
		}

	}
	var closestFix string
	smallestValue := float32(math.MaxFloat32)
	for fix, multiple := range fixes {
		for _, info := range multiple {
			if info.Type == ZoneBasedFix {
				dist := math.NMDistance2LL(ac.Position(), database.Fixes[fix].Location)
				if dist < smallestValue {
					smallestValue = dist
					closestFix = fix
				}
			}
		}
	}

	if closestFix == "" {
		lg.Errorf("No fix for %v/%v. Route: %v.", ac.Callsign, ac.Squawk, ac.Nav.Waypoints)
	}
	return closestFix
}

func (fp *FlightPlan) STARS() *STARSFlightPlan {
	return &STARSFlightPlan{
		FlightPlan: *fp,
	}
}

// identifier can be bcn or callsign
func (w *World) getSTARSFlightPlan(identifier string) (*STARSFlightPlan, error) {
	_, stars := w.FacilityComputers(FIXME)
	squawk, err := ParseSquawk(identifier)
	if err == nil { // Squawk code was entered
		fp, ok := stars.ContainedPlans[squawk]
		if ok { // The flight plan is stored in the system
			return fp, nil
		}
	} else { // Callsign was entered
		for _, plan := range stars.ContainedPlans {
			if plan.Callsign == identifier { // We have this plan in our system
				return plan, nil
			}
		}
	}
	return nil, ErrSTARSNoFlight
}

// This should be facility-defined in the json file, but for now it's 30nm near their departure airport
func (ac *Aircraft) inAcquisitionArea(w *World) bool {
	if ac != nil {
		ap := w.GetAirport(ac.FlightPlan.DepartureAirport)
		ap2 := w.GetAirport(ac.FlightPlan.ArrivalAirport)
		if ap != nil {
			if math.NMDistance2LL(ap.Location, ac.Position()) <= 2 && !ac.inDropArea(w) {
				return true
			}
		}
		if ap2 != nil {
			if math.NMDistance2LL(ap2.Location, ac.Position()) <= 2 && !ac.inDropArea(w) {
				return true
			}
		}
	}
	return false
}

func (ac *Aircraft) inDropArea(w *World) bool {
	ap := w.GetAirport(ac.FlightPlan.DepartureAirport)
	ap2 := w.GetAirport(ac.FlightPlan.ArrivalAirport)
	if (ap != nil && math.NMDistance2LL(ap.Location, ac.Position()) <= 1) || (ap2 != nil && math.NMDistance2LL(ap2.Location, ac.Position()) <= 1) {
		if (ap != nil && ac.Altitude() <= float32(database.Airports[ac.FlightPlan.DepartureAirport].Elevation+50)) ||
			ac.Altitude() <= float32(database.Airports[ac.FlightPlan.ArrivalAirport].Elevation+50) {
			return true
		}
	}

	return false
}

func (w *World) parseAbbreviatedFPFields(fields []string) AbbreviatedFPFields {
	output := AbbreviatedFPFields{}
	if len(fields[0]) >= 2 && len(fields[0]) <= 7 && unicode.IsLetter(rune(fields[0][0])) {
		output.ACID = fields[0]

	} else {
		output.Error = ErrSTARSIllegalACID
		return output
	}

	for _, field := range fields[1:] { // fields[0] is always the ACID
		sq, err := ParseSquawk(field) // See if it's a BCN
		if err == nil {
			output.BCN = sq
			continue
		}
		if len(field) == 2 { // See if its specifying the controlling position
			output.ControllingPosition = field
			continue
		}
		if len(field) <= 2 { // See if it's specifying the type of flight. No errors for this because this could turn into a scratchpad
			if len(field) == 1 {
				switch field {
				case "A":
					output.TypeOfFlight = "arrival"
				case "P":
					output.TypeOfFlight = "departure"
				case "E":
					output.TypeOfFlight = "overflight"
				}
			} else if len(field) == 2 { // Type first, then airport id
				types := []string{"A", "P", "E"}
				if slices.Contains(types, field[:1]) {
					output.TypeOfFlight = field[:1]
					output.DepartureAirport = field[1:]
					continue
				}
			}
		}

		badScratchpads := []string{"NAT", "CST", "AMB", "RDR", "ADB", "XXX"}
		if strings.HasPrefix(field, STARSTriangleCharacter) && len(field) > 3 && len(field) <= 5 || (len(field) <= 6 && w.STARSFacilityAdaptation.AllowLongScratchpad[0]) { // See if it's specifying the SC1

			if slices.Contains(badScratchpads, field) {
				output.Error = ErrSTARSIllegalScratchpad
				return output
			}
			if util.IsAllNumbers(field[len(field)-3:]) {
				output.Error = ErrSTARSIllegalScratchpad
			}
			output.SC1 = field
		}
		if strings.HasPrefix(field, "+") && len(field) > 2 && (len(field) <= 4 || (len(field) <= 5 && w.STARSFacilityAdaptation.AllowLongScratchpad[1])) { // See if it's specifying the SC1
			if slices.Contains(badScratchpads, field) {
				output.Error = ErrSTARSIllegalScratchpad
				return output
			}
			if util.IsAllNumbers(field[len(field)-3:]) {
				output.Error = ErrSTARSIllegalScratchpad
			}
			output.SC2 = field
		}
		if acFields := strings.Split(field, "/"); len(field) >= 4 { // See if it's specifying the type of flight
			switch len(acFields) {
			case 1: // Just the AC Type
				if _, ok := database.AircraftPerformance[field]; !ok { // AC doesn't exist
					output.Error = ErrSTARSIllegalACType
					continue
				} else {
					output.AircraftType = field
					continue
				}
			case 2: // Either a formation number with the ac type or a ac type with a equipment suffix
				if all := util.IsAllNumbers(acFields[0]); all { // Formation number
					if !unicode.IsLetter(rune(acFields[1][0])) {
						output.Error = ErrSTARSCommandFormat
						return output
					}
					if _, ok := database.AircraftPerformance[acFields[1]]; !ok { // AC doesn't exist
						output.Error = ErrSTARSIllegalACType // This error is informational. Shouldn't end the entire function. Just this switch statement
						continue
					}
					output.AircraftType = field
				} else { // AC Type with equipment suffix
					if len(acFields[1]) > 1 || !util.IsAllLetters(acFields[1]) {
						output.Error = ErrSTARSCommandFormat
						return output
					}
					if _, ok := database.AircraftPerformance[acFields[0]]; !ok { // AC doesn't exist
						output.Error = ErrSTARSIllegalACType
						continue
					}
					output.AircraftType = field
				}
			case 3:
				if len(acFields[2]) > 1 || !util.IsAllLetters(acFields[2]) {
					output.Error = ErrSTARSCommandFormat
					return output
				}
				if !unicode.IsLetter(rune(acFields[1][0])) {
					output.Error = ErrSTARSCommandFormat
					return output
				}
				if _, ok := database.AircraftPerformance[acFields[1]]; !ok { // AC doesn't exist
					output.Error = ErrSTARSIllegalACType
					break
				}
				output.AircraftType = field
			}
			continue
		}
		if len(field) == 3 && util.IsAllNumbers(field) {
			output.RequestedALT = field
			continue
		}
		if len(field) == 2 {
			if field[0] != '.' {
				output.Error = ErrSTARSCommandFormat
				return output
			}
			switch field[1] {
			case 'V':
				output.Rules = VFR
				break // This is the last entry, so we can break here
			case 'P':
				output.Rules = VFR // vfr on top
				break
			case 'E':
				output.Rules = IFR // enroute
				break
			default:
				output.Error = ErrSTARSIllegalValue
				return output
			}
		}

	}
	return output
}
*/
