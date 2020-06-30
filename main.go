package main

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/andlabs/ui"
	_ "github.com/andlabs/ui/winmanifest"
	"github.com/tealeg/xlsx"
)

const (
	CREW_FILE         = "info.xlsx"
	SCHEDULE_FILE     = "Troop to Task.xlsx"
	FULL_DATE_FORMAT  = "Jan 02 06"
	INPUT_DATE_FORMAT = "1/2/2006"

	RANK_COL       = 1
	LAST_NAME_COL  = 2
	FIRST_NAME_COL = 3

	INFO_FIRST_LAST_NAME_COL = 0
	INFO_RANK_COL            = 1
	INFO_HOURS_COL           = 2

	NUMBER_OF_MAINTENANCE_FLIGHTS = 1
	NUMBER_OF_TRAINING_FLIGHTS    = 3
)

var (
	daysByMonth = map[string]int{
		"Jan": 31,
		"Feb": 28, //TODO: Or 29
		"Mar": 31,
		"Apr": 30,
		"May": 31,
		"Jun": 30,
		"Jul": 31,
		"Aug": 31,
		"Sep": 30,
		"Oct": 31,
		"Nov": 30,
		"Dec": 31,
	}

	statusWhiteList = map[string]bool{
		"PCs": true, //TODO: Normalize these to be lowercase
		"PIs": true,
		"FEs": true,
		"CEs": true,
	}

	canFlyMap = map[string]bool{
		"":    true,
		"F":   true,
		"AMR": true,
	}

	rawDataRegexp = regexp.MustCompile(`\[\$\-[0-9]+\]([A-Za-z\\\-[0-9]+)`)

	timesByIndex = map[int]string{
		0: "0900", //MAINTENANCE
		1: "0800", //TRAINING
		2: "1000", //TRAINING
		3: "1100", //TRAINING
		4: "1200", //NORMAL
		5: "1200", //NORMAL
		6: "1700", //NORMAL
	}

	flightTypesByIndex = map[int]string{
		0: "MAINTENANCE",
		1: "TRAINING",
		2: "TRAINING",
		3: "TRAINING",
		4: "NORMAL",
		5: "NORMAL",
		6: "NORMAL",
	}

	sheetHeading = []string{
		"Date",
		"Flight Type",
		"Time",
		"Status",
		"Rank",
		"First Name",
		"Last Name",
	}

	mainwin          *ui.Window
	numFlightsByDate = make(map[string]int)
	dates            = []string{}
	inputComplete    bool
	fullCrew         = []string{}
	piCrew           = []string{}
	pcCrew           = []string{}
	feCrew           = []string{}
	ceCrew           = []string{}
)

/*********Primary Structs*********/
type SchedulePayload struct {
	CrewAvailability []*CrewAvailability
}

type CrewAvailability struct {
	FirstName   string
	LastName    string
	Rank        string
	Status      string
	Availabilty map[string]bool
}

// type Schedule struct {
// 	AvailabilityByDate map[string]bool //Key: Date (format: Jan 01 06); Value: crew member is or is not available
// }

// type CrewPayload struct {
// 	CrewMembers []*CrewMember
// }

/*********Secondary Structs*********/

type FlightSchedules struct {
	Flights []*Flight
}

type Flight struct {
	Type string //MAINTENANCE, TRAINING, or NORMAL
	Date string
	Time string
	PC   *CrewMember
	PIs  []*CrewMember
	FE   *CrewMember
	CEs  []*CrewMember // No CE required for maintainence flights
}

type CrewMember struct {
	FirstName string
	LastName  string
	Rank      string
	Status    string
}

/*
	- Normal Flights Require: PC, a PI, an FE, and a CE; Maintenance Flights (1 per day) Require: PC, a PI, and FE; Training Flights (3 per day) Require: Either 3 CEs OR 2 PIs
	- PCs, PIs, and Fes that occur higher up in the input file are higher priority
	- People can't be on 2 flights in the same day
	- If you can have their rank, name, and identifier show up in the block (if applicable), but also have the text be editable, that'd be awesome. If not no worries
	- Also sometimes there is more than one PI, and more than 1 FE or CE because of training, so if you can add multiple people into slots that'd be helpful too
*/

//Input: SchedulePayload (list of crew availability)
//Output: scheduled flights
func main() {
	ui.Main(setupUI)
}

func setupUI() {
	mainwin = ui.NewWindow("Flight Scheduler", 640, 480, true)
	mainwin.OnClosing(func(*ui.Window) bool {
		ui.Quit()
		return true
	})
	ui.OnShouldQuit(func() bool {
		mainwin.Destroy()
		return true
	})

	mainwin.SetMargined(true)
	tab := ui.NewTab()
	mainwin.SetChild(tab)

	tab.Append("Choose a Date", makeDatePage())
	tab.SetMargined(0, true)

	mainwin.Show()

	ticker := time.NewTicker(5 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				if inputComplete {
					schedulePayload, err := payloadsFromXLSX([]string{ /*CREW_FILE, */ SCHEDULE_FILE})
					fatalIf(err)

					flightSchedules, err := schedulePayload.calculateFlightSchedules()
					fatalIf(err)

					err = exportXLSXResult(flightSchedules)
					fatalIf(err)

					ui.Quit()
					break
				}
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
}

func makeDatePage() ui.Control {
	vbox := ui.NewVerticalBox()
	vbox.SetPadded(true)

	vbox.Append(ui.NewLabel("Flight schedules will be generated a week from the date you choose."), false)

	datePicker := ui.NewDatePicker()
	vbox.Append(datePicker, false)

	button := ui.NewButton("Next")
	button.OnClicked(func(*ui.Button) {
		date := datePicker.Time()

		for i := 0; i < 7; i++ {
			dateString := fmt.Sprintf("%d/%d/%d", date.Month(), date.Day(), date.Year())
			numFlightsByDate[dateString] = 3 // 3 by default
			dates = append(dates, dateString)

			date = date.AddDate(0, 0, 1)
		}

		tab := ui.NewTab()
		mainwin.SetChild(tab)

		tab.Append("Number of Flights", makeFlightNumberPage())
		tab.SetMargined(0, true)

		tab.Show()
	})
	vbox.Append(button, false)

	return vbox
}

func makeFlightNumberPage() ui.Control {
	vbox := ui.NewVerticalBox()
	vbox.SetPadded(true)

	vbox.Append(ui.NewLabel("Choose the number of normal flights for each day (excluding 1 maintenance flight and 3 training sims which are always scheduled)"), false)

	/*****         0          *****/
	date0 := dates[0]
	flights := numFlightsByDate[date0]
	hbox := ui.NewHorizontalBox()

	hbox.Append(ui.NewLabel(fmt.Sprintf("%s:", date0)), false)

	numFlightsInput0 := ui.NewSpinbox(0, 100)
	numFlightsInput0.SetValue(flights)
	numFlightsInput0.OnChanged(func(*ui.Spinbox) {
		numFlightsByDate[date0] = numFlightsInput0.Value()
	})
	hbox.Append(numFlightsInput0, false)

	vbox.Append(hbox, false)

	/*****         1          *****/
	date1 := dates[1]
	flights = numFlightsByDate[date1]
	hbox = ui.NewHorizontalBox()

	hbox.Append(ui.NewLabel(fmt.Sprintf("%s:", date1)), false)

	numFlightsInput1 := ui.NewSpinbox(0, 100)
	numFlightsInput1.SetValue(flights)
	numFlightsInput1.OnChanged(func(*ui.Spinbox) {
		numFlightsByDate[date1] = numFlightsInput1.Value()
	})
	hbox.Append(numFlightsInput1, false)

	vbox.Append(hbox, false)

	/*****         2          *****/
	date2 := dates[2]
	flights = numFlightsByDate[date2]
	hbox = ui.NewHorizontalBox()

	hbox.Append(ui.NewLabel(fmt.Sprintf("%s:", date2)), false)

	numFlightsInput2 := ui.NewSpinbox(0, 100)
	numFlightsInput2.SetValue(flights)
	numFlightsInput2.OnChanged(func(*ui.Spinbox) {
		numFlightsByDate[date2] = numFlightsInput2.Value()
	})
	hbox.Append(numFlightsInput2, false)

	vbox.Append(hbox, false)

	/*****         3          *****/
	date3 := dates[3]
	flights = numFlightsByDate[date3]
	hbox = ui.NewHorizontalBox()

	hbox.Append(ui.NewLabel(fmt.Sprintf("%s:", date3)), false)

	numFlightsInput3 := ui.NewSpinbox(0, 100)
	numFlightsInput3.SetValue(flights)
	numFlightsInput3.OnChanged(func(*ui.Spinbox) {
		numFlightsByDate[date3] = numFlightsInput3.Value()
	})
	hbox.Append(numFlightsInput3, false)

	vbox.Append(hbox, false)

	/*****         4          *****/
	date4 := dates[4]
	flights = numFlightsByDate[date4]
	hbox = ui.NewHorizontalBox()

	hbox.Append(ui.NewLabel(fmt.Sprintf("%s:", date4)), false)

	numFlightsInput4 := ui.NewSpinbox(0, 100)
	numFlightsInput4.SetValue(flights)
	numFlightsInput4.OnChanged(func(*ui.Spinbox) {
		numFlightsByDate[date4] = numFlightsInput4.Value()
	})
	hbox.Append(numFlightsInput4, false)

	vbox.Append(hbox, false)

	/*****         5          *****/
	date5 := dates[5]
	flights = numFlightsByDate[date5]
	hbox = ui.NewHorizontalBox()

	hbox.Append(ui.NewLabel(fmt.Sprintf("%s:", date5)), false)

	numFlightsInput5 := ui.NewSpinbox(0, 100)
	numFlightsInput5.SetValue(flights)
	numFlightsInput5.OnChanged(func(*ui.Spinbox) {
		numFlightsByDate[date5] = numFlightsInput5.Value()
	})
	hbox.Append(numFlightsInput5, false)

	vbox.Append(hbox, false)

	/*****         6          *****/
	date6 := dates[6]
	flights = numFlightsByDate[date6]
	hbox = ui.NewHorizontalBox()

	hbox.Append(ui.NewLabel(fmt.Sprintf("%s:", date6)), false)

	numFlightsInput6 := ui.NewSpinbox(0, 100)
	numFlightsInput6.SetValue(flights)
	numFlightsInput6.OnChanged(func(*ui.Spinbox) {
		numFlightsByDate[date6] = numFlightsInput6.Value()
	})
	hbox.Append(numFlightsInput6, false)

	vbox.Append(hbox, false)

	button := ui.NewButton("Done")
	button.OnClicked(func(*ui.Button) {
		tab := ui.NewTab()
		mainwin.SetChild(tab)

		tab.Append("Generating", makeGeneratingPage())
		tab.SetMargined(0, true)

		tab.Show()

		inputComplete = true
	})

	vbox.Append(button, false)

	return vbox
}

func makeGeneratingPage() ui.Control {
	vbox := ui.NewVerticalBox()
	vbox.SetPadded(true)

	vbox.Append(ui.NewLabel("Flight schedules are being generated."), false)
	vbox.Append(ui.NewLabel("This window will close when complete."), false)

	return vbox
}

func NewSchedulePayload(crewAvailability []*CrewAvailability) *SchedulePayload {
	return &SchedulePayload{
		CrewAvailability: crewAvailability,
	}
}

// func NewCrewPayload(crewMembers []*CrewMember) *CrewPayload {
// 	return &CrewPayload{
// 		CrewMembers: crewMembers,
// 	}
// }

func addSheetHeading(sheet *xlsx.Sheet, heading []string) {
	row := sheet.AddRow()
	for _, headingEntry := range heading {
		cell := row.AddCell()
		cell.Value = headingEntry
	}
}

func exportXLSXResult(flightSchedules *FlightSchedules) error {
	file := xlsx.NewFile()

	sheet, err := file.AddSheet("Flights")
	if err != nil {
		return err
	}

	addSheetHeading(sheet, sheetHeading)
	for _, flight := range flightSchedules.Flights {
		row := sheet.AddRow()
		cell := row.AddCell()
		cell.Value = flight.Date
		// if i == 0 {
		// 	cell.Value = flight.Date
		// } else {
		// 	cell.Value = "-"
		// }

		cell = row.AddCell()
		cell.Value = flight.Type

		cell = row.AddCell()
		cell.Value = flight.Time

		row = sheet.AddRow()

		if flight.PC != nil {
			addSingleCrew(row, flight.PC)
			row = sheet.AddRow()
		}

		if flight.PIs != nil && len(flight.PIs) > 0 {
			addMultipleCrew(sheet, row, flight.PIs)
			row = sheet.AddRow()
		}

		if flight.FE != nil {
			addSingleCrew(row, flight.FE)
			row = sheet.AddRow()
		}

		if flight.CEs != nil && len(flight.CEs) > 0 {
			addMultipleCrew(sheet, row, flight.CEs)
			row = sheet.AddRow()
		}
	}

	if _, err := os.Stat("files/"); os.IsNotExist(err) {
		os.Mkdir("files/", 0700)
	}

	err = file.Save("files/FlightSchedules.xlsx")
	if err != nil {
		return err
	}

	return nil
}

func addSingleCrew(row *xlsx.Row, crew *CrewMember) {
	cell := row.AddCell()
	cell.Value = "-"
	cell = row.AddCell()
	cell.Value = "-"
	cell = row.AddCell()
	cell.Value = "-"
	cell = row.AddCell()
	cell.Value = crew.Status
	cell = row.AddCell()
	cell.Value = crew.Rank
	cell = row.AddCell()
	cell.Value = crew.FirstName
	cell = row.AddCell()
	cell.Value = crew.LastName
}

func addMultipleCrew(sheet *xlsx.Sheet, row *xlsx.Row, crewMembers []*CrewMember) {
	for i, crew := range crewMembers {
		if i > 0 {
			row = sheet.AddRow()
		}

		cell := row.AddCell()
		cell.Value = "-"
		cell = row.AddCell()
		cell.Value = "-"
		cell = row.AddCell()
		cell.Value = "-"
		cell = row.AddCell()
		cell.Value = crew.Status
		cell = row.AddCell()
		cell.Value = crew.Rank
		cell = row.AddCell()
		cell.Value = crew.FirstName
		cell = row.AddCell()
		cell.Value = crew.LastName
	}
}

// By default, we need
func (s *SchedulePayload) calculateFlightSchedules() (*FlightSchedules, error) {
	var (
		crewHasFlight          = make(map[string]bool)
		currentFlightDate      string
		crewHasAFlightThisWeek = map[string]map[string]bool{ //Key: status; Value: list of crew names
			"PI": make(map[string]bool),
			"PC": make(map[string]bool),
			"FE": make(map[string]bool),
			"CE": make(map[string]bool),
		}
	)

	flightSchedules, err := initializeFlightSchedules()
	if err != nil {
		return nil, err
	}

	for i, flight := range flightSchedules.Flights {
		if currentFlightDate != flight.Date { //clear map when the date changes
			for k := range crewHasFlight {
				delete(crewHasFlight, k)
			}
		}

		currentFlightDate = flight.Date
		for _, crew := range s.CrewAvailability {
			crewName := fmt.Sprintf("%s %s", crew.FirstName, crew.LastName)

			if available, ok := crew.Availabilty[flight.Date]; ok && available && !crewHasFlight[crewName] && !crewHasAFlightThisWeek[crew.Status][crewName] { // Crew member is available for that day :)
				spotOccupied := isSpotOccupied(flightSchedules, crew.Status, flight.Type, i)
				if !spotOccupied {
					switch crew.Status {
					case "PC":
						flightSchedules.Flights[i].PC = &CrewMember{
							FirstName: crew.FirstName,
							LastName:  crew.LastName,
							Rank:      crew.Rank,
							Status:    crew.Status,
						}
						break
					case "PI":
						flightSchedules.Flights[i].PIs = append(flightSchedules.Flights[i].PIs, &CrewMember{
							FirstName: crew.FirstName,
							LastName:  crew.LastName,
							Rank:      crew.Rank,
							Status:    crew.Status,
						})
						break
					case "FE":
						flightSchedules.Flights[i].FE = &CrewMember{
							FirstName: crew.FirstName,
							LastName:  crew.LastName,
							Rank:      crew.Rank,
							Status:    crew.Status,
						}
						break
					case "CE":
						flightSchedules.Flights[i].CEs = append(flightSchedules.Flights[i].CEs, &CrewMember{
							FirstName: crew.FirstName,
							LastName:  crew.LastName,
							Rank:      crew.Rank,
							Status:    crew.Status,
						})
						break
					}
					crewHasFlight[crewName] = true
					crewHasAFlightThisWeek[crew.Status][crewName] = true

					if len(fullCrew) == len(crewHasAFlightThisWeek) { //Clear when we get at least one flight for everyone this week
						for k := range crewHasAFlightThisWeek {
							delete(crewHasAFlightThisWeek, k)
						}
					}
					if 0.9*float64(len(piCrew)) >= float64(len(crewHasAFlightThisWeek["PI"])) {
						crewHasAFlightThisWeek["PI"] = make(map[string]bool)
					}
					if 0.9*float64(len(pcCrew)) >= float64(len(crewHasAFlightThisWeek["PC"])) {
						crewHasAFlightThisWeek["PC"] = make(map[string]bool)

					}
					if 0.9*float64(len(feCrew)) >= float64(len(crewHasAFlightThisWeek["FE"])) {
						crewHasAFlightThisWeek["FE"] = make(map[string]bool)

					}
					if 0.9*float64(len(ceCrew)) >= float64(len(crewHasAFlightThisWeek["CE"])) {
						crewHasAFlightThisWeek["CE"] = make(map[string]bool)
					}
				}
			}
		}
	}

	return flightSchedules, nil
}

func isSpotOccupied(flightSchedules *FlightSchedules, crewStatus string, flightType string, flightIndex int) bool {
	flight := flightSchedules.Flights[flightIndex]

	switch crewStatus {
	case "PC":
		return flight.PC != nil
		break
	case "PI":
		switch flight.Type {
		case "MAINTENANCE", "NORMAL":
			return flight.PIs != nil && len(flight.PIs) == 1
			break
		case "TRAINING":
			if flightIndex%2 == 0 {
				return flight.PIs != nil && len(flight.PIs) == 2
			} else {
				return flight.PIs != nil && len(flight.PIs) == 1
			}
			break
		}
		break
	case "FE":
		return flight.FE != nil
		break
	case "CE":
		switch flight.Type {
		case "MAINTENANCE":
			return true
			break
		case "TRAINING":
			if flightIndex%2 == 0 {
				return flight.CEs != nil && len(flight.CEs) == 1
			} else {
				return flight.CEs != nil && len(flight.CEs) == 3
			}
			break
		case "NORMAL":
			return flight.CEs != nil && len(flight.CEs) == 1
			break
		}
		break
	}

	return false
}

func initializeFlightSchedules() (*FlightSchedules, error) {
	flightSchedules := &FlightSchedules{Flights: []*Flight{}}

	for _, date := range dates {
		inputDate, err := time.Parse(INPUT_DATE_FORMAT, date)
		if err != nil {
			return nil, err
		}

		inputDateString := fmt.Sprintf("%d/%d/%d", inputDate.Month(), inputDate.Day(), inputDate.Year())
		fullDate := inputDate.Format(FULL_DATE_FORMAT)

		flights := numFlightsByDate[inputDateString]

		for i := 0; i < flights+NUMBER_OF_MAINTENANCE_FLIGHTS+NUMBER_OF_TRAINING_FLIGHTS; i++ {
			flightSchedules.Flights = append(flightSchedules.Flights, &Flight{
				Type: flightTypesByIndex[i],
				Date: fullDate,
				Time: timesByIndex[i],
			})
		}
	}

	return flightSchedules, nil
}

func payloadsFromXLSX(fileNames []string) (*SchedulePayload, error) {
	var (
		schedulePayload *SchedulePayload
		// crewPayload     *CrewPayload
		err error
	)

	for _, fileName := range fileNames {
		log.Println("Reading", fileName)
		file, err := xlsx.OpenFile(fileName)
		if err != nil {
			return nil, err
		}

		switch fileName {
		case SCHEDULE_FILE:
			schedulePayload, err = schedulePayloadFromXLSX(file)
			fatalIf(err)
			break
			// case CREW_FILE:
			// 	crewPayload, err = crewPayloadFromXLSX(file)
			// 	fatalIf(err)
			// 	break
		}
	}

	err = checkPayloadsForFunnyBusiness(schedulePayload)

	return schedulePayload, err
}

func schedulePayloadFromXLSX(file *xlsx.File) (*SchedulePayload, error) {
	var schedulePayload *SchedulePayload

	if len(file.Sheets) > 0 {
		sheet := file.Sheets[len(file.Sheets)-1]

		scheduleMap, err := getScheduleMap(sheet)
		if err != nil {
			return nil, err
		}

		schedulePayload, err = createSchedulePayload(sheet, scheduleMap)
		if err != nil {
			return nil, err
		}
	}

	return schedulePayload, nil
}

func createSchedulePayload(sheet *xlsx.Sheet, scheduleMap map[int]string) (*SchedulePayload, error) {
	var (
		crewAvailabilities = []*CrewAvailability{}
		currentStatus      string
	)

	for i, row := range sheet.Rows {
		if i < 4 {
			continue
		}

		//Do a check for status
		firstCellVal, err := row.Cells[0].FormattedValue()
		if err != nil {
			return nil, err
		}

		firstCellVal = strings.TrimSpace(firstCellVal)

		if _, ok := statusWhiteList[firstCellVal]; ok {
			currentStatus = firstCellVal
			continue
		} else if len(firstCellVal) == 0 {
			break
		}

		rank, err := row.Cells[RANK_COL].FormattedValue()
		if err != nil {
			return nil, err
		}

		firstName, err := row.Cells[FIRST_NAME_COL].FormattedValue()
		if err != nil {
			return nil, err
		}

		lastName, err := row.Cells[LAST_NAME_COL].FormattedValue()
		if err != nil {
			return nil, err
		}

		availability := make(map[string]bool)
		for j := 5; j < len(row.Cells); j++ {
			cell := row.Cells[j]
			avail, err := cell.FormattedValue() //availability: Everything means busy or can't fly except F, AMR, or blank
			if err != nil {
				return nil, err
			}

			if date, ok := scheduleMap[j]; ok {
				if _, canFly := canFlyMap[avail]; canFly {
					availability[date] = true
				} else {
					availability[date] = false
				}
			}
		}

		crewAvailabilities = append(crewAvailabilities,
			&CrewAvailability{
				FirstName:   strings.ReplaceAll(firstName, "*", ""),
				LastName:    strings.ReplaceAll(lastName, "*", ""),
				Rank:        rank,
				Status:      strings.TrimSuffix(currentStatus, "s"),
				Availabilty: availability,
			},
		)

		fullCrew = append(fullCrew, fmt.Sprintf("%s %s", strings.ReplaceAll(firstName, "*", ""), strings.ReplaceAll(lastName, "*", "")))
		switch strings.TrimSuffix(currentStatus, "s") {
		case "PC":
			pcCrew = append(pcCrew, fmt.Sprintf("%s %s", strings.ReplaceAll(firstName, "*", ""), strings.ReplaceAll(lastName, "*", "")))
			break
		case "PI":
			piCrew = append(piCrew, fmt.Sprintf("%s %s", strings.ReplaceAll(firstName, "*", ""), strings.ReplaceAll(lastName, "*", "")))
			break
		case "FE":
			feCrew = append(feCrew, fmt.Sprintf("%s %s", strings.ReplaceAll(firstName, "*", ""), strings.ReplaceAll(lastName, "*", "")))
			break
		case "CE":
			ceCrew = append(ceCrew, fmt.Sprintf("%s %s", strings.ReplaceAll(firstName, "*", ""), strings.ReplaceAll(lastName, "*", "")))
			break
		}
	}

	return NewSchedulePayload(crewAvailabilities), nil
}

func getScheduleMap(sheet *xlsx.Sheet) (map[int]string, error) {
	var (
		startingColByDate = make(map[string]int)
		scheduleMap       = make(map[int]string) //Return value - Key: Column of the cell that refers to that date; Value: Date (format Jan 01 06)
	)

	for i, row := range sheet.Rows {
		if i == 2 {
			break
		}
		for j, cell := range row.Cells {
			val, err := cell.FormattedValue()
			if err != nil {
				return nil, err
			}

			if i == 0 { // Find month-year strings, and their starting columns
				if rawDataRegexp.MatchString(val) {
					val = rawDataRegexp.ReplaceAllString(val, "$1")
					val = strings.ReplaceAll(val, `\`, "")
					startingColByDate[val] = j
				}
			} else if i == 1 { // Find days of week and days of month in the cell below
				runes := []rune(val)
				dayOfMonth := string(runes[0:2])

				for date, startingCol := range startingColByDate {
					splitDate := strings.Split(date, "-")
					if len(splitDate) == 2 {
						month := splitDate[0] //Jan-06 -> Jan
						year := splitDate[1]
						if daysInMonth, ok := daysByMonth[month]; ok {
							startCol, endCol := startingCol, startingCol+daysInMonth //TODO: Still need to handle leap year

							if j >= startCol && j <= endCol {
								fullDate := fmt.Sprintf("%s %s %s", month, dayOfMonth, year)
								scheduleMap[j] = fullDate
							}
						}
					}
				}
			}
		}
	}

	return scheduleMap, nil
}

// func crewPayloadFromXLSX(file *xlsx.File) (*CrewPayload, error) {
// 	var (
// 		crewMembers = []*CrewMember{}
// 	)

// 	if len(file.Sheets) > 0 {
// 		sheet := file.Sheets[0]
// 		for _, row := range sheet.Rows {
// 			var (
// 				firstName string
// 				lastName  string
// 				rank      string
// 				hours     float64
// 			)

// 			firstLast, err := row.Cells[INFO_FIRST_LAST_NAME_COL].FormattedValue()
// 			if err != nil {
// 				return nil, err
// 			}

// 			nameSplit := strings.Split(firstLast, ", ")
// 			if len(nameSplit) == 2 {
// 				lastName = nameSplit[0]
// 				firstName = nameSplit[1]
// 			}

// 			rank, err = row.Cells[INFO_RANK_COL].FormattedValue()
// 			if err != nil {
// 				return nil, err
// 			}

// 			hours, err = row.Cells[INFO_RANK_COL].Float()
// 			if err != nil {
// 				return nil, err
// 			}

// 			crewMembers = append(crewMembers,
// 				&CrewMember{
// 					FirstName: firstName,
// 					LastName:  lastName,
// 					Rank:      rank,
// 					Hours:     hours,
// 				},
// 			)
// 		}
// 	}

// 	return NewCrewPayload(crewMembers), nil
// }

func checkPayloadsForFunnyBusiness(schedulePayload *SchedulePayload) error {
	var err error

	if schedulePayload == nil {
		err = fmt.Errorf("Error parsing %s; ", SCHEDULE_FILE)
	}
	// if crewPayload == nil {
	// 	if err != nil {
	// 		err = fmt.Errorf("%sError parsing %s;", err.Error(), CREW_FILE)
	// 	} else {
	// 		err = fmt.Errorf("Error parsing %s; ", CREW_FILE)
	// 	}
	// }

	return err
}

func fatalIf(err error) {
	if err != nil {
		log.Fatalf(err.Error())
	}
}
