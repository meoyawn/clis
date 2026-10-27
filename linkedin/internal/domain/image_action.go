package domain

import "fmt"

type ImageAction struct{ value string }

const (
	imageUploadAction   = "com.linkedin.sdui.requests.media.registerMediaUpload"
	imageRegisterAction = "com.linkedin.sdui.requests.mediaproduct.RegisterImageMedia"
)

var allowedImageActions = [...]string{imageUploadAction, imageRegisterAction}

func ImageUploadAction() ImageAction   { return ImageAction{imageUploadAction} }
func ImageRegisterAction() ImageAction { return ImageAction{imageRegisterAction} }
func (a ImageAction) String() string   { return a.value }
func NewImageActionFromValue(value string) (ImageAction, error) {
	switch value {
	case imageUploadAction:
		return ImageUploadAction(), nil
	case imageRegisterAction:
		return ImageRegisterAction(), nil
	default:
		return ImageAction{}, fmt.Errorf("invalid image action %q; expected %v", value, allowedImageActions)
	}
}
