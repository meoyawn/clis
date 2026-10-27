package domain

import "fmt"

// PostBindingType distinguishes the wire representation of a composer value.
type PostBindingType struct{ value string }

const (
	textBinding       = "textModelForWrite"
	stringBinding     = "stringValue"
	longBinding       = "signedLongValue"
	boolBinding       = "booleanValue"
	expressionBinding = "expression"
	stringListBinding = "stringListValue"
	mediaListBinding  = "mediaListValue"
)

var allowedPostBindings = [...]string{textBinding, stringBinding, longBinding, boolBinding, expressionBinding, stringListBinding, mediaListBinding}

func PostMediaListBinding() PostBindingType { return PostBindingType{mediaListBinding} }

func PostTextBinding() PostBindingType       { return PostBindingType{textBinding} }
func PostStringBinding() PostBindingType     { return PostBindingType{stringBinding} }
func PostLongBinding() PostBindingType       { return PostBindingType{longBinding} }
func PostBoolBinding() PostBindingType       { return PostBindingType{boolBinding} }
func PostExpressionBinding() PostBindingType { return PostBindingType{expressionBinding} }
func PostStringListBinding() PostBindingType { return PostBindingType{stringListBinding} }
func (t PostBindingType) String() string     { return t.value }

func NewPostBindingTypeFromValue(value string) (PostBindingType, error) {
	switch value {
	case textBinding:
		return PostTextBinding(), nil
	case stringBinding:
		return PostStringBinding(), nil
	case longBinding:
		return PostLongBinding(), nil
	case boolBinding:
		return PostBoolBinding(), nil
	case expressionBinding:
		return PostExpressionBinding(), nil
	case stringListBinding:
		return PostStringListBinding(), nil
	case mediaListBinding:
		return PostMediaListBinding(), nil
	default:
		return PostBindingType{}, fmt.Errorf("invalid post binding type %q; expected one of %v", value, allowedPostBindings)
	}
}
