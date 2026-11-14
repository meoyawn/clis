package domain

import "fmt"

type Operation struct{ value string }

const (
	profileOperation   = "UserByScreenName"
	timelineOperation  = "UserOriginalsTimeline"
	readOperation      = "TweetDetail"
	createOperation    = "CreateTweet"
	deleteOperation    = "deleteTweetMutation"
	repliesOperation   = "UserRepliesTimeline"
	chatUsersOperation = "GetUsersByIdsForXChat"
	chatReadOperation  = "GetConversationPageQuery"
	chatSendOperation  = "SendMessageCreateMutation"
)

const legacyDeleteOperation = "DeleteTweet"

var allowedOperations = [...]string{profileOperation, timelineOperation, readOperation, createOperation, repliesOperation, deleteOperation, legacyDeleteOperation, chatUsersOperation, chatReadOperation, chatSendOperation}

func RepliesOperation() Operation { return Operation{repliesOperation} }

func ProfileOperation() Operation   { return Operation{profileOperation} }
func TimelineOperation() Operation  { return Operation{timelineOperation} }
func ReadOperation() Operation      { return Operation{readOperation} }
func CreateOperation() Operation    { return Operation{createOperation} }
func DeleteOperation() Operation    { return Operation{deleteOperation} }
func ChatUsersOperation() Operation { return Operation{chatUsersOperation} }
func ChatReadOperation() Operation  { return Operation{chatReadOperation} }
func ChatSendOperation() Operation  { return Operation{chatSendOperation} }
func (o Operation) String() string  { return o.value }

func LegacyDeleteOperation() Operation { return Operation{legacyDeleteOperation} }

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
	case deleteOperation:
		return DeleteOperation(), nil
	case legacyDeleteOperation:
		return LegacyDeleteOperation(), nil
	case repliesOperation:
		return RepliesOperation(), nil
	case chatUsersOperation:
		return ChatUsersOperation(), nil
	case chatReadOperation:
		return ChatReadOperation(), nil
	case chatSendOperation:
		return ChatSendOperation(), nil
	default:
		return Operation{}, fmt.Errorf("unsupported operation %q; expected one of %v", value, allowedOperations)
	}
}
