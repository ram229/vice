// panes.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	Name() string

	Duplicate(nameAsCopy bool) Pane

	Activate(cs *ColorScheme)
	Deactivate()
	Update(updates *WorldUpdates)

	// Note: the caller should treat the returned DrawLists as read-only;
	// the Pane may for example leave parts unchanged from frame to frame.
	Draw(ctx *PaneContext) []*DrawList
}

type PaneUIDrawer interface {
	DrawUI()
}

type PaneContext struct {
	paneExtent        Extent2D
	parentPaneExtent  Extent2D
	fullDisplayExtent Extent2D // FIXME: this is only needed for mouse shenanegans.

	highDPIScale float32

	platform Platform
	cs       *ColorScheme
	mouse    *MouseState
}

type MouseState struct {
	pos           [2]float32
	down          [mouseButtonCount]bool
	clicked       [mouseButtonCount]bool
	released      [mouseButtonCount]bool
	doubleClicked [mouseButtonCount]bool
	dragging      [mouseButtonCount]bool
	dragDelta     [2]float32
	wheel         [2]float32
}

const (
	mouseButtonPrimary   = 0
	mouseButtonSecondary = 1
	mouseButtonTertiary  = 2
	mouseButtonCount     = 3
)

func (ctx *PaneContext) InitializeMouse() {
	ctx.mouse = &MouseState{}

	// Convert to pane coordinates:
	// imgui gives us the mouse position w.r.t. the full window, so we need
	// to subtract out displayExtent.p0 to get coordinates w.r.t. the
	// current pane.  Further, it has (0,0) in the upper left corner of the
	// window, so we need to flip y w.r.t. the full window resolution.
	pos := imgui.MousePos()
	ctx.mouse.pos[0] = pos.X - ctx.paneExtent.p0[0]
	ctx.mouse.pos[1] = ctx.fullDisplayExtent.p1[1] - 1 - ctx.paneExtent.p0[1] - pos.Y

	io := imgui.CurrentIO()
	wx, wy := io.MouseWheel()
	ctx.mouse.wheel = [2]float32{wx, -wy}

	for b := 0; b < mouseButtonCount; b++ {
		ctx.mouse.down[b] = imgui.IsMouseDown(b)
		ctx.mouse.released[b] = imgui.IsMouseReleased(b)
		ctx.mouse.clicked[b] = imgui.IsMouseClicked(b)
		ctx.mouse.doubleClicked[b] = imgui.IsMouseDoubleClicked(b)
		ctx.mouse.dragging[b] = imgui.IsMouseDragging(b, 0)
		if ctx.mouse.dragging[b] {
			delta := imgui.MouseDragDelta(b, 0.)
			// Negate y to go to pane coordinates
			ctx.mouse.dragDelta = [2]float32{delta.X, -delta.Y}
			imgui.ResetMouseDragDelta(b)
		}
	}
}

type AirportInfoPane struct {
	Airports map[string]interface{}

	ShowTime        bool
	ShowMETAR       bool
	ShowATIS        bool
	ShowUncleared   bool
	ShowDepartures  bool
	ShowDeparted    bool
	ShowArrivals    bool
	ShowControllers bool

	FontIdentifier FontIdentifier
	font           *Font

	lastUpdate        time.Time
	lastExtent        Extent2D
	forceUpdate       bool
	lastTextColor     RGB
	lastSelectedColor RGB

	td TextDrawable
	dl DrawList
}

func NewAirportInfoPane() *AirportInfoPane {
	// Reasonable (I hope) defaults...
	font := GetDefaultFont()
	return &AirportInfoPane{
		Airports:        make(map[string]interface{}),
		ShowTime:        true,
		ShowMETAR:       true,
		ShowATIS:        true,
		ShowUncleared:   true,
		ShowDepartures:  true,
		ShowDeparted:    true,
		ShowArrivals:    true,
		ShowControllers: true,

		font:           font,
		FontIdentifier: font.id,
	}
}

func (a *AirportInfoPane) Duplicate(nameAsCopy bool) Pane {
	dupe := *a
	dupe.td = TextDrawable{}
	dupe.dl = DrawList{}
	return &dupe
}

func (a *AirportInfoPane) Activate(cs *ColorScheme) {
	if a.font = GetFont(a.FontIdentifier); a.font == nil {
		a.font = GetDefaultFont()
		a.FontIdentifier = a.font.id
	}
	// FIXME: temporary transition
	if a.Airports == nil {
		a.Airports = make(map[string]interface{})
	}
}

func (a *AirportInfoPane) Deactivate() {}

func (a *AirportInfoPane) Name() string {
	n := "Airport Information"
	if len(a.Airports) > 0 {
		n += ": " + strings.Join(SortedMapKeys(a.Airports), ",")
	}
	return n
}

func (a *AirportInfoPane) DrawUI() {
	a.Airports = drawAirportSelector(a.Airports, "Airports")
	if newFont, changed := DrawFontPicker(&a.FontIdentifier, "Font"); changed {
		a.font = newFont
	}
	imgui.Checkbox("Show time", &a.ShowTime)
	imgui.Checkbox("Show weather", &a.ShowMETAR)
	imgui.Checkbox("Show ATIS", &a.ShowATIS)
	imgui.Checkbox("Show uncleared aircraft", &a.ShowUncleared)
	imgui.Checkbox("Show aircraft to depart", &a.ShowDepartures)
	imgui.Checkbox("Show departed aircraft", &a.ShowDeparted)
	imgui.Checkbox("Show arriving aircraft", &a.ShowArrivals)
	imgui.Checkbox("Show controllers", &a.ShowControllers)
}

type Arrival struct {
	aircraft     *Aircraft
	distance     float32
	sortDistance float32
}

type Departure struct {
	*Aircraft
}

func getDistanceSortedArrivals() []Arrival {
	var arr []Arrival
	for _, ac := range world.aircraft {
		if !positionConfig.IsActiveAirport(ac.flightPlan.arrive) || ac.OnGround() || ac.LostTrack() {
			continue
		}

		pos := ac.Position()
		// Filter ones where we don't have a valid position
		if pos[0] != 0 && pos[1] != 0 {
			dist := nmdistance2ll(world.FAA.airports[ac.flightPlan.arrive].location, pos)
			sortDist := dist + float32(ac.Altitude())/300.
			arr = append(arr, Arrival{aircraft: ac, distance: dist, sortDistance: sortDist})
		}
	}

	sort.Slice(arr, func(i, j int) bool {
		return arr[i].sortDistance < arr[j].sortDistance
	})

	return arr
}

func (a *AirportInfoPane) Update(updates *WorldUpdates) {
	if updates != nil && !updates.NoUpdates() {
		a.forceUpdate = true
	}
}

func (a *AirportInfoPane) Draw(ctx *PaneContext) []*DrawList {
	cs := ctx.cs

	// Try to only update the DrawList once a second. However, two number
	// things can invalidate it before that:
	//
	// 1. World updates: we don't try to be clever and see if they are
	// aircraft we care about but just go ahead if there are any.
	// 2. Changes to the text color or selected text color.
	//
	// Changes to the background color are fine since we can always stuff
	// that into the draw list without invalidating anything.  Note that
	// this means that things like ATIS and METAR updates will be slightly
	// delayed, though by no more than 1s.
	d := time.Since(a.lastUpdate)
	if d.Seconds() < 1 && !a.forceUpdate && cs.Text.Equals(a.lastTextColor) &&
		cs.SelectedDataBlock.Equals(a.lastSelectedColor) &&
		a.lastExtent.Width() == ctx.paneExtent.Width() &&
		a.lastExtent.Height() == ctx.paneExtent.Height() {
		// Don't reset the draw list or text drawable, though do update the
		// background color
		a.dl.clearColor = cs.Background
		a.dl.clear = true
		return []*DrawList{&a.dl}
	}
	a.lastUpdate = time.Now()
	a.lastExtent = ctx.paneExtent
	a.forceUpdate = false
	a.lastTextColor = cs.Text
	a.lastSelectedColor = cs.SelectedDataBlock

	var str strings.Builder
	style := TextStyle{font: a.font, color: cs.Text}
	var strs []string
	var styles []TextStyle

	flush := func() {
		if str.Len() == 0 {
			return
		}
		strs = append(strs, str.String())
		str.Reset()
		styles = append(styles, style)
		style = TextStyle{font: a.font, color: cs.Text} // a reasonable default
	}

	if a.ShowTime {
		str.WriteString(time.Now().UTC().Format("Time: 15:04:05Z\n\n"))
	}

	if a.ShowMETAR && len(world.metar) > 0 {
		var metar []METAR
		for _, m := range world.metar {
			metar = append(metar, m)
		}
		sort.Slice(metar, func(i, j int) bool {
			return metar[i].airport < metar[j].airport
		})
		str.WriteString("Weather:\n")
		for _, m := range metar {
			str.WriteString(fmt.Sprintf("  %4s ", m.airport))
			flush()
			style.color = cs.TextHighlight
			str.WriteString(fmt.Sprintf("%s %s ", m.altimeter, m.wind))
			flush()
			str.WriteString(fmt.Sprintf("%s\n", m.weather))
		}
		str.WriteString("\n")
	}

	if a.ShowATIS {
		var atis []string
		for issuer, a := range world.atis {
			if positionConfig.IsActiveAirport(issuer[:4]) {
				atis = append(atis, fmt.Sprintf("  %-12s: %s", issuer, a))
			}
		}
		if len(atis) > 0 {
			sort.Strings(atis)
			str.WriteString("ATIS:\n")
			for _, a := range atis {
				str.WriteString(a)
				str.WriteString("\n")
			}
			str.WriteString("\n")
		}
	}

	var uncleared, departures, airborne []Departure
	for _, ac := range world.aircraft {
		if ac.LostTrack() {
			continue
		}

		if positionConfig.IsActiveAirport(ac.flightPlan.depart) {
			if ac.OnGround() {
				if ac.assignedSquawk == 0 {
					uncleared = append(uncleared, Departure{Aircraft: ac})
				} else {
					departures = append(departures, Departure{Aircraft: ac})
				}
			} else {
				airborne = append(airborne, Departure{Aircraft: ac})
			}
		}
	}

	if a.ShowUncleared && len(uncleared) > 0 {
		str.WriteString("Uncleared:\n")
		sort.Slice(uncleared, func(i, j int) bool {
			return uncleared[i].Callsign() < uncleared[j].Callsign()
		})
		for _, ac := range uncleared {
			str.WriteString(fmt.Sprintf("  %-8s %3s %4s-%4s %8s %5d\n", ac.Callsign(),
				ac.flightPlan.rules, ac.flightPlan.depart, ac.flightPlan.arrive,
				ac.flightPlan.actype, ac.flightPlan.altitude))

			// Route
			if len(ac.flightPlan.route) > 0 {
				str.WriteString("    ")
				str.WriteString(ac.flightPlan.route)
				str.WriteString("\n")
			}
		}
		str.WriteString("\n")
	}

	if a.ShowDepartures && len(departures) > 0 {
		str.WriteString("Departures:\n")
		sort.Slice(departures, func(i, j int) bool {
			return departures[i].Callsign() < departures[j].Callsign()
		})
		for _, ac := range departures {
			route := ac.flightPlan.route
			if len(route) > 10 {
				route = route[:10]
				route += ".."
			}
			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %5d %12s", ac.Callsign(),
				ac.flightPlan.rules, ac.flightPlan.depart, ac.flightPlan.actype,
				ac.scratchpad, ac.flightPlan.altitude, route))

			// Make sure the squawk is good
			if ac.mode != Charlie || ac.squawk != ac.assignedSquawk {
				str.WriteString(" sq:")
				if ac.mode != Charlie {
					str.WriteString("[C]")
				}
				if ac.squawk != ac.assignedSquawk {
					str.WriteString(ac.assignedSquawk.String())
				}
			}
			str.WriteString("\n")
		}
		str.WriteString("\n")
	}

	if a.ShowDeparted && len(airborne) > 0 {
		sort.Slice(airborne, func(i, j int) bool {
			ai := &airborne[i]
			di := nmdistance2ll(world.FAA.airports[ai.flightPlan.arrive].location, ai.Position())
			aj := &airborne[j]
			dj := nmdistance2ll(world.FAA.airports[aj.flightPlan.arrive].location, aj.Position())
			return di < dj
		})

		str.WriteString("Departed:\n")
		for _, ac := range airborne {
			alt := ac.Altitude()
			alt = (alt + 50) / 100 * 100
			var clearedAlt string
			if ac.tempAltitude != 0 {
				clearedAlt = fmt.Sprintf("%5dT", ac.tempAltitude)
			} else {
				clearedAlt = fmt.Sprintf("%5d ", ac.flightPlan.altitude)
			}
			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %s %5d\n", ac.Callsign(),
				ac.flightPlan.rules, ac.flightPlan.depart, ac.flightPlan.actype,
				ac.scratchpad, clearedAlt, alt))
		}
		str.WriteString("\n")
	}

	arr := getDistanceSortedArrivals()
	if a.ShowArrivals && len(arr) > 0 {
		str.WriteString("Arrivals:\n")
		for _, a := range arr {
			ac := a.aircraft
			alt := ac.Altitude()
			alt = (alt + 50) / 100 * 100
			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %5d  %5d %3dnm\n", ac.Callsign(),
				ac.flightPlan.rules, ac.flightPlan.arrive, ac.flightPlan.actype, ac.scratchpad,
				ac.tempAltitude, alt, int(a.distance)))
		}
		str.WriteString("\n")
	}

	if a.ShowControllers {
		var cstr strings.Builder

		sorted := SortedMapKeys(world.controllers)
		for _, suffix := range []string{"CTR", "APP", "DEP", "TWR", "GND", "DEL", "FSS", "ATIS", "OBS"} {
			first := true
			for _, c := range sorted {
				if !strings.HasSuffix(c, suffix) {
					continue
				}

				ctrl := world.controllers[c]
				if ctrl.frequency == 0 {
					continue
				}

				if first {
					cstr.WriteString(fmt.Sprintf("  %-4s  ", suffix))
					first = false
				} else {
					cstr.WriteString("        ")
				}
				cstr.WriteString(fmt.Sprintf(" %-12s %s", ctrl.callsign, ctrl.frequency))

				if ctrl.position != nil {
					cstr.WriteString(fmt.Sprintf(" %-3s %s", ctrl.position.sectorId, ctrl.position.scope))
				}
				cstr.WriteString("\n")
			}
		}

		if cstr.Len() > 0 {
			str.WriteString("Controllers:\n")
			str.WriteString(cstr.String())
			str.WriteString("\n")
		}
	}

	flush()

	a.dl.Reset()
	a.dl.clear = true
	a.dl.clearColor = cs.Background
	a.dl.UseWindowCoordiantes(ctx.paneExtent.Width(), ctx.paneExtent.Height())

	a.td.Reset()
	sz2 := float32(a.font.size) / 2
	a.td.AddTextMulti(strs, [2]float32{sz2, ctx.paneExtent.Height() - sz2}, styles)
	a.dl.AddText(a.td)

	return []*DrawList{&a.dl}
}

///////////////////////////////////////////////////////////////////////////
// EmptyPane

type EmptyPane struct {
	dl DrawList
}

func NewEmptyPane() *EmptyPane { return &EmptyPane{} }

func (ep *EmptyPane) Activate(cs *ColorScheme)     {}
func (ep *EmptyPane) Deactivate()                  {}
func (ep *EmptyPane) Update(updates *WorldUpdates) {}

func (ep *EmptyPane) Duplicate(nameAsCopy bool) Pane { return &EmptyPane{} }
func (ep *EmptyPane) Name() string                   { return "(Empty)" }

func (ep *EmptyPane) Draw(ctx *PaneContext) []*DrawList {
	ep.dl = DrawList{clear: true, clearColor: ctx.cs.Background}
	return []*DrawList{&ep.dl}
}

///////////////////////////////////////////////////////////////////////////
// FlightPlanPane

type FlightPlanPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	ShowRemarks bool

	td TextDrawable
	dl DrawList
}

func NewFlightPlanPane() *FlightPlanPane {
	font := GetDefaultFont()
	return &FlightPlanPane{FontIdentifier: font.id, font: font}
}

func (fp *FlightPlanPane) Activate(cs *ColorScheme) {
	if fp.font = GetFont(fp.FontIdentifier); fp.font == nil {
		fp.font = GetDefaultFont()
		fp.FontIdentifier = fp.font.id
	}
}

func (fp *FlightPlanPane) Deactivate()                  {}
func (fp *FlightPlanPane) Update(updates *WorldUpdates) {}

func (fp *FlightPlanPane) DrawUI() {
	imgui.Checkbox("Show remarks", &fp.ShowRemarks)
	if newFont, changed := DrawFontPicker(&fp.FontIdentifier, "Font"); changed {
		fp.font = newFont
	}
}

func (fp *FlightPlanPane) Duplicate(nameAsCopy bool) Pane {
	return &FlightPlanPane{FontIdentifier: fp.FontIdentifier, font: fp.font}
}

func (fp *FlightPlanPane) Name() string { return "Flight Plan" }

func (fp *FlightPlanPane) Draw(ctx *PaneContext) []*DrawList {
	contents := ""

	if positionConfig.selectedAircraft != nil {
		contents, _ = positionConfig.selectedAircraft.GetFormattedFlightPlan(fp.ShowRemarks)
	}

	fp.td.Reset()
	sz2 := float32(fp.font.size) / 2
	fp.td.AddText(contents, [2]float32{sz2, ctx.paneExtent.Height() - sz2},
		TextStyle{font: fp.font, color: ctx.cs.Text})

	fp.dl.Reset()
	fp.dl.AddText(fp.td)
	fp.dl.clear = true
	fp.dl.clearColor = ctx.cs.Background
	fp.dl.UseWindowCoordiantes(ctx.paneExtent.Width(), ctx.paneExtent.Height())

	return []*DrawList{&fp.dl}
}

///////////////////////////////////////////////////////////////////////////
// NotesViewPane

type NotesViewItem struct {
	Note    *Note
	Visible bool
}

type NotesViewPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	Items []NotesViewItem

	selectedRow    int
	dragStartIndex int
	dragCopy       []NotesViewItem

	td TextDrawable
	dl DrawList
}

func NewNotesViewPane() *NotesViewPane {
	font := GetDefaultFont()
	return &NotesViewPane{FontIdentifier: font.id, font: font}
}

func (nv *NotesViewPane) Activate(cs *ColorScheme) {
	if nv.font = GetFont(nv.FontIdentifier); nv.font == nil {
		nv.font = GetDefaultFont()
		nv.FontIdentifier = nv.font.id
	}
	nv.selectedRow = -1
}

func (nv *NotesViewPane) Deactivate() {}

func (nv *NotesViewPane) Update(updates *WorldUpdates) {}

func (nv *NotesViewPane) Duplicate(nameAsCopy bool) Pane {
	n := &NotesViewPane{FontIdentifier: nv.FontIdentifier, font: nv.font}
	n.Items = make([]NotesViewItem, len(nv.Items))
	copy(n.Items, nv.Items)
	return n
}

func (nv *NotesViewPane) DrawUI() {
	// Following @unpacklo, https://gist.github.com/unpacklo/f4af1d688237a7d367f9
	hovered := -1
	flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV // | imgui.TableFlagsRowBg
	if imgui.BeginTableV(fmt.Sprintf("NotesView##%p", nv), 2, flags, imgui.Vec2{}, 0.0) {
		imgui.TableSetupColumnV("Title", imgui.TableColumnFlagsWidthStretch, 0., 0)
		imgui.TableSetupColumnV("Visible", imgui.TableColumnFlagsWidthFixed, 20., 0)
		for i, item := range nv.Items {
			imgui.TableNextColumn()
			selFlags := imgui.SelectableFlagsSpanAllColumns
			if imgui.SelectableV(item.Note.Title, i == nv.selectedRow, selFlags, imgui.Vec2{}) {
				nv.selectedRow = i
			}
			imgui.SetItemAllowOverlap() // don't let the selectable steal the checkbox's clicks
			if imgui.IsItemHoveredV(imgui.HoveredFlagsRectOnly) {
				hovered = i
			}

			imgui.TableNextColumn()
			imgui.Checkbox(fmt.Sprintf("##Active%d", i), &nv.Items[i].Visible)
			imgui.TableNextRow()
		}

		if hovered != -1 {
			if imgui.IsMouseDragging(0, 1.) {
				// reorder list
				//lg.Printf("reorder %d -> %d", nv.dragStartIndex, hovered)

				item := nv.Items[nv.dragStartIndex]
				if hovered < nv.dragStartIndex {
					// number moving
					n := nv.dragStartIndex - hovered
					// move forward one
					copy(nv.Items[hovered+1:], nv.Items[hovered:hovered+n])
				} else if hovered > nv.dragStartIndex {
					// number moving
					n := hovered - nv.dragStartIndex
					// move back one
					copy(nv.Items[nv.dragStartIndex:nv.dragStartIndex+n], nv.Items[nv.dragStartIndex+1:])
				}
				nv.Items[hovered] = item
				nv.dragStartIndex = hovered
			} else if imgui.IsMouseDown(0) {
				// drag logic
				//lg.Printf("drag %d", hovered)
				nv.dragStartIndex = hovered
				nv.dragCopy = make([]NotesViewItem, len(nv.Items))
				copy(nv.dragCopy, nv.Items)
			}
		}
		imgui.EndTable()
	}

	if imgui.BeginCombo("Add note", "") {
		notes := globalConfig.NotesSortedByTitle()
		for _, n := range notes {
			if imgui.Selectable(n.Title) {
				nv.Items = append(nv.Items, NotesViewItem{Note: n, Visible: false})
			}
		}
		imgui.EndCombo()
	}

	disableRemove := nv.selectedRow == -1 || nv.selectedRow >= len(nv.Items)
	if disableRemove {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}
	if imgui.Button("Remove") {
		if nv.selectedRow < len(nv.Items)-1 {
			copy(nv.Items[nv.selectedRow:], nv.Items[nv.selectedRow+1:])
		}
		nv.Items = nv.Items[:len(nv.Items)-1]
	}
	if disableRemove {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}

	if newFont, changed := DrawFontPicker(&nv.FontIdentifier, "Font"); changed {
		nv.font = newFont
	}
}

func (nv *NotesViewPane) Name() string { return "Notes View" }

func (nv *NotesViewPane) Draw(ctx *PaneContext) []*DrawList {
	s := ""
	for _, item := range nv.Items {
		if !item.Visible {
			continue
		}
		// Indent each line by two spaces
		lines := strings.Split(item.Note.Contents, "\n")
		contents := "  " + strings.Join(lines, "\n  ")
		s += item.Note.Title + "\n" + contents + "\n\n"
	}

	nv.td.Reset()
	sz2 := float32(nv.font.size) / 2
	nv.td.AddText(s, [2]float32{sz2, ctx.paneExtent.Height() - sz2},
		TextStyle{font: nv.font, color: ctx.cs.Text})

	nv.dl.Reset()
	nv.dl.AddText(nv.td)
	nv.dl.clear = true
	nv.dl.clearColor = ctx.cs.Background
	nv.dl.UseWindowCoordiantes(ctx.paneExtent.Width(), ctx.paneExtent.Height())

	return []*DrawList{&nv.dl}
}

///////////////////////////////////////////////////////////////////////////
// PerformancePane

type PerformancePane struct {
	disableVSync bool

	nFrames        uint64
	initialMallocs uint64

	// exponential averages of various time measurements (in ms)
	processMessages float32
	drawPanes       float32
	drawImgui       float32

	FontIdentifier FontIdentifier
	font           *Font

	td TextDrawable
	dl DrawList
}

func NewPerformancePane() *PerformancePane {
	font := GetDefaultFont()
	return &PerformancePane{FontIdentifier: font.id, font: font}
}

func (pp *PerformancePane) Duplicate(nameAsCopy bool) Pane {
	return &PerformancePane{FontIdentifier: pp.FontIdentifier, font: pp.font}
}

func (pp *PerformancePane) Activate(cs *ColorScheme) {
	if pp.font = GetFont(pp.FontIdentifier); pp.font == nil {
		pp.font = GetDefaultFont()
		lg.Printf("want %+v got %+v", pp.FontIdentifier, pp.font)
		pp.FontIdentifier = pp.font.id
	}
}

func (pp *PerformancePane) Deactivate()                  {}
func (pp *PerformancePane) Update(updates *WorldUpdates) {}

func (pp *PerformancePane) Name() string { return "Performance Information" }

func (pp *PerformancePane) DrawUI() {
	if newFont, changed := DrawFontPicker(&pp.FontIdentifier, "Font"); changed {
		pp.font = newFont
	}
	if imgui.Checkbox("Disable vsync", &pp.disableVSync) {
		platform.EnableVSync(!pp.disableVSync)
	}
}

func (pp *PerformancePane) Draw(ctx *PaneContext) []*DrawList {
	const initialFrames = 10

	pp.nFrames++

	var perf strings.Builder
	perf.Grow(512)

	// First framerate
	perf.WriteString(fmt.Sprintf("Average %.1f ms/frame (%.1f FPS)",
		1000/imgui.CurrentIO().Framerate(), imgui.CurrentIO().Framerate()))

	// Runtime breakdown
	update := func(d time.Duration, stat *float32) float32 {
		dms := float32(d.Microseconds()) / 1000. // duration in ms
		*stat = .99**stat + .01*dms
		return *stat
	}
	perf.WriteString(fmt.Sprintf("\nmsgs %.2fms draw panes %.2fms draw gui %.2fms",
		update(stats.processMessages, &pp.processMessages),
		update(stats.drawPanes, &pp.drawPanes),
		update(stats.drawImgui, &pp.drawImgui)))

	// Memory stats
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	if pp.nFrames == initialFrames {
		pp.initialMallocs = mem.Mallocs
	}
	mallocsPerFrame := uint64(0)
	if pp.nFrames > initialFrames {
		mallocsPerFrame = (mem.Mallocs - pp.initialMallocs) / (pp.nFrames - initialFrames)
	}
	active1000s := (mem.Mallocs - mem.Frees) / 1000
	perf.WriteString(fmt.Sprintf("\nMallocs/frame %d (%dk active) %d MB in use",
		mallocsPerFrame, active1000s, mem.HeapAlloc/(1024*1024)))

	// Rendering stats
	perf.WriteString(fmt.Sprintf("\n%dk verts, %d draw calls, %dk lines, %dk tris, %d chars",
		stats.draw.vertices/1000, stats.draw.drawCalls, stats.draw.lines/1000,
		stats.draw.tris/1000, stats.draw.chars))

	pp.td.Reset()
	sz2 := float32(pp.font.size) / 2
	pp.td.AddText(perf.String(), [2]float32{sz2, ctx.paneExtent.Height() - sz2},
		TextStyle{font: pp.font, color: ctx.cs.Text})

	pp.dl.Reset()
	pp.dl.AddText(pp.td)
	pp.dl.clear = true
	pp.dl.clearColor = ctx.cs.Background
	pp.dl.UseWindowCoordiantes(ctx.paneExtent.Width(), ctx.paneExtent.Height())

	return []*DrawList{&pp.dl}
}

///////////////////////////////////////////////////////////////////////////
// ReminderPane

type ReminderPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	dl DrawList
}

type ReminderItem interface {
	Draw(text func(s string, color RGB), ctx *PaneContext)
}

type TimerReminderItem struct {
	end      time.Time
	note     string
	lastBeep time.Time
}

func (t *TimerReminderItem) Draw(text func(s string, color RGB), ctx *PaneContext) {
	now := time.Now()
	if now.After(t.end) {
		// Beep every 15s until cleared
		if now.Sub(t.lastBeep) > 15*time.Second {
			globalConfig.AudioSettings.HandleEvent(AudioEventTimerFinished)
			t.lastBeep = now
		}

		flashcycle := now.Second()
		if flashcycle&1 == 0 {
			text("--:-- ", ctx.cs.TextHighlight)
		} else {
			text("      ", ctx.cs.Text)
		}
	} else {
		remaining := t.end.Sub(now)
		remaining = remaining.Round(time.Second)
		minutes := int(remaining.Minutes())
		remaining -= time.Duration(minutes) * time.Minute
		seconds := int(remaining.Seconds())
		text(fmt.Sprintf("%02d:%02d ", minutes, seconds), ctx.cs.Text)
	}
	text(t.note, ctx.cs.Text)
}

type ToDoReminderItem struct {
	note string
}

func (t *ToDoReminderItem) Draw(text func(s string, color RGB), ctx *PaneContext) {
	text(t.note, ctx.cs.Text)
}

func NewReminderPane() *ReminderPane {
	font := GetDefaultFont()
	return &ReminderPane{FontIdentifier: font.id, font: font}
}

func (rp *ReminderPane) Duplicate(nameAsCopy bool) Pane {
	return &ReminderPane{FontIdentifier: rp.FontIdentifier, font: rp.font}
}

func (rp *ReminderPane) Activate(cs *ColorScheme) {
	if rp.font = GetFont(rp.FontIdentifier); rp.font == nil {
		rp.font = GetDefaultFont()
		rp.FontIdentifier = rp.font.id
	}
}

func (rp *ReminderPane) Deactivate()                  {}
func (rp *ReminderPane) Update(updates *WorldUpdates) {}
func (rp *ReminderPane) Name() string                 { return "Reminders" }

func (rp *ReminderPane) DrawUI() {
	if newFont, changed := DrawFontPicker(&rp.FontIdentifier, "Font"); changed {
		rp.font = newFont
	}
}

func (rp *ReminderPane) Draw(ctx *PaneContext) []*DrawList {
	// We're not using imgui, so we have to handle hovered and clicked by
	// ourselves.  Here are the key quantities:
	indent := int(rp.font.size / 2) // left and top spacing
	checkWidth, _ := rp.font.BoundText(FontAwesomeIconSquare, 0)
	spaceWidth := int(rp.font.LookupGlyph(' ').AdvanceX)
	textIndent := indent + checkWidth + spaceWidth

	lineHeight := rp.font.size + 2
	// Current cursor position
	x, y := textIndent, int(ctx.paneExtent.Height())-indent

	// Reset the drawlist before we get going.
	rp.dl.Reset()

	text := func(s string, color RGB) {
		td := TextDrawable{}
		td.AddText(s, [2]float32{float32(x), float32(y)}, TextStyle{font: rp.font, color: color})
		rp.dl.AddText(td)

		bx, _ := rp.font.BoundText(s, 0)
		x += bx
	}
	hovered := func() bool {
		return ctx.mouse != nil && ctx.mouse.pos[1] < float32(y) && ctx.mouse.pos[1] >= float32(y-lineHeight)
	}
	buttonDown := func() bool {
		return hovered() && ctx.mouse.down[0]
	}
	released := func() bool {
		return hovered() && ctx.mouse.released[0]
	}

	var items []ReminderItem
	for i := range positionConfig.timers {
		items = append(items, &positionConfig.timers[i])
	}
	for i := range positionConfig.todos {
		items = append(items, &positionConfig.todos[i])
	}

	removeItem := len(items) // invalid -> remove nothing
	for i, item := range items {
		if hovered() {
			// Draw the selection box; we want this for both hovered() and
			// buttonDown(), so handle it separately. (Note that
			// buttonDown() implies hovered().)
			rect := LinesDrawable{}
			width := ctx.paneExtent.Width()
			rect.AddPolyline([2]float32{float32(indent) / 2, float32(y)}, ctx.cs.Text,
				[][2]float32{[2]float32{0, 0},
					[2]float32{width - float32(indent), 0},
					[2]float32{width - float32(indent), float32(-lineHeight)},
					[2]float32{0, float32(-lineHeight)}})
			rp.dl.lines = append(rp.dl.lines, rect)
		}

		// Draw a suitable box
		x = indent
		if buttonDown() {
			text(FontAwesomeIconCheckSquare, ctx.cs.Text)
		} else {
			text(FontAwesomeIconSquare, ctx.cs.Text)
		}

		if released() {
			removeItem = i
		}

		x = textIndent
		item.Draw(text, ctx)
		y -= lineHeight
	}

	if removeItem < len(positionConfig.timers) {
		if removeItem == 0 {
			positionConfig.timers = positionConfig.timers[1:]
		} else {
			positionConfig.timers = append(positionConfig.timers[:removeItem],
				positionConfig.timers[removeItem+1:]...)
		}
	} else {
		removeItem -= len(positionConfig.timers)
		if removeItem < len(positionConfig.todos) {
			if removeItem == 0 {
				positionConfig.todos = positionConfig.todos[1:]
			} else {
				positionConfig.todos = append(positionConfig.todos[:removeItem],
					positionConfig.todos[removeItem+1:]...)
			}
		}
	}

	rp.dl.clear = true
	rp.dl.clearColor = ctx.cs.Background
	rp.dl.UseWindowCoordiantes(ctx.paneExtent.Width(), ctx.paneExtent.Height())

	return []*DrawList{&rp.dl}
}
