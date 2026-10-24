package domain

import "fmt"

type MainService struct{ value string }

const (
	electricityValue = "Электроснабжение день"
	hotWaterValue    = "Подогрев воды"
	coldWaterValue   = "Холодное водоснабжение"
)

func Electricity() MainService { return MainService{electricityValue} }
func HotWater() MainService    { return MainService{hotWaterValue} }
func ColdWater() MainService   { return MainService{coldWaterValue} }

func NewMainServiceFromValue(value string) (MainService, error) {
	switch value {
	case electricityValue, hotWaterValue, coldWaterValue:
		return MainService{value}, nil
	default:
		return MainService{}, fmt.Errorf("unsupported main service %q", value)
	}
}

func (service MainService) String() string { return service.value }
