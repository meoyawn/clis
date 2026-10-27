package domain

import "fmt"

type Operation struct{ value string }

const (
	profileOperation  = "UserByScreenName"
	timelineOperation = "UserOriginalsTimeline"
	readOperation     = "TweetDetail"
	createOperation   = "CreateTweet"
	repliesOperation  = "UserRepliesTimeline"
)

var allowedOperations = [...]string{profileOperation, timelineOperation, readOperation, createOperation, repliesOperation}

func RepliesOperation() Operation { return Operation{repliesOperation} }

func ProfileOperation() Operation  { return Operation{profileOperation} }
func TimelineOperation() Operation { return Operation{timelineOperation} }
func ReadOperation() Operation     { return Operation{readOperation} }
func CreateOperation() Operation   { return Operation{createOperation} }
func (o Operation) String() string { return o.value }

func NewOperationFromValue(value string) (Operation, error) {
	switch value {
	case profileOperation:
		return ProfileOperation(), nil
	case timelineOperation:
		return TimelineOperation(), nil
	case readOperation:
		return ReadOperation(), nil
	case createOperation:
		return CreateOperation(), nil
	case repliesOperation:
		return RepliesOperation(), nil
	default:
		return Operation{}, fmt.Errorf("unsupported operation %q; expected one of %v", value, allowedOperations)
	}
}
