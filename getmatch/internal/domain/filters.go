package domain

import "fmt"

type Level struct{ value string }

const (
	allLevelsValue = "all"
	juniorValue    = "junior"
	middleValue    = "middle"
	seniorValue    = "senior"
	leadValue      = "lead"
	cLevelValue    = "c_level"
)

var allowedLevelValues = [...]string{allLevelsValue, juniorValue, middleValue, seniorValue, leadValue, cLevelValue}

func AllLevels() Level { return Level{value: allLevelsValue} }
func Junior() Level    { return Level{value: juniorValue} }
func Middle() Level    { return Level{value: middleValue} }
func Senior() Level    { return Level{value: seniorValue} }
func Lead() Level      { return Level{value: leadValue} }
func CLevel() Level    { return Level{value: cLevelValue} }

func NewLevelFromValue(value string) (Level, error) {
	switch value {
	case allLevelsValue:
		return AllLevels(), nil
	case juniorValue:
		return Junior(), nil
	case middleValue:
		return Middle(), nil
	case seniorValue:
		return Senior(), nil
	case leadValue:
		return Lead(), nil
	case cLevelValue:
		return CLevel(), nil
	}
	return Level{}, fmt.Errorf("invalid level %q, allowed values: %v", value, allowedLevelValues)
}
func (v Level) String() string { return v.value }

type City struct{ value string }

const (
	allCitiesValue       = "all"
	moscowValue          = "moscow"
	saintPetersburgValue = "spb"
	otherRussiaValue     = "other_russia"
)

var allowedCityValues = [...]string{allCitiesValue, moscowValue, saintPetersburgValue, otherRussiaValue}

func AllCities() City       { return City{value: allCitiesValue} }
func Moscow() City          { return City{value: moscowValue} }
func SaintPetersburg() City { return City{value: saintPetersburgValue} }
func OtherRussia() City     { return City{value: otherRussiaValue} }

func NewCityFromValue(value string) (City, error) {
	switch value {
	case allCitiesValue:
		return AllCities(), nil
	case moscowValue:
		return Moscow(), nil
	case saintPetersburgValue:
		return SaintPetersburg(), nil
	case otherRussiaValue:
		return OtherRussia(), nil
	}
	return City{}, fmt.Errorf("invalid city %q, allowed values: %v", value, allowedCityValues)
}
func (v City) String() string { return v.value }

type WorkFormat struct{ value string }

const (
	allWorkFormatsValue = "all"
	remoteValue         = "remote"
	officeHybridValue   = "office_hybrid"
)

var allowedWorkFormatValues = [...]string{allWorkFormatsValue, remoteValue, officeHybridValue}

func AllWorkFormats() WorkFormat { return WorkFormat{value: allWorkFormatsValue} }
func Remote() WorkFormat         { return WorkFormat{value: remoteValue} }
func OfficeHybrid() WorkFormat   { return WorkFormat{value: officeHybridValue} }

func NewWorkFormatFromValue(value string) (WorkFormat, error) {
	switch value {
	case allWorkFormatsValue:
		return AllWorkFormats(), nil
	case remoteValue:
		return Remote(), nil
	case officeHybridValue:
		return OfficeHybrid(), nil
	}
	return WorkFormat{}, fmt.Errorf("invalid workformat %q, allowed values: %v", value, allowedWorkFormatValues)
}
func (v WorkFormat) String() string { return v.value }
