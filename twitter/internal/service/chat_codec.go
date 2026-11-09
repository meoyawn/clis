package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf8"
)

// X Chat's public MessageEntryHolder, MessageCreateEvent and MessageEvent
// adapters use Thrift's binary protocol. This codec only writes plain text.
type chatStruct map[uint16]any

func chatField(kind byte, id uint16, value []byte) []byte {
	return append(binary.BigEndian.AppendUint16([]byte{kind}, id), value...)
}

func chatString(id uint16, value []byte) []byte {
	return chatField(11, id, append(binary.BigEndian.AppendUint32(nil, uint32(len(value))), value...))
}

func plainChatEvent(text string) (string, error) {
	if !utf8.ValidString(text) || len(text) == 0 || len(text) > 10000 {
		return "", fmt.Errorf("message must contain 1–10000 UTF-8 bytes")
	}
	contents := append(chatString(1, []byte(text)), 0)
	entry := append(chatField(12, 1, contents), 0)
	holder := append(chatField(12, 1, entry), 0)
	event := chatString(100, holder)
	event = append(event, chatField(2, 102, []byte{1})...)
	event = append(event, chatField(2, 105, []byte{0})...)
	event = append(event, chatField(8, 106, []byte{0, 0, 0, 1})...)
	return base64.StdEncoding.EncodeToString(append(event, 0)), nil
}

func decodeChatStruct(data []byte) (chatStruct, error) {
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("chat event exceeds 1 MiB")
	}
	r := bytes.NewReader(data)
	v, err := readChatValue(r, 12, 0)
	if err != nil {
		return nil, err
	}
	if r.Len() != 0 {
		return nil, fmt.Errorf("trailing data in Chat event")
	}
	return v.(chatStruct), nil
}

func chatUint(r *bytes.Reader, size int) (uint64, error) {
	var data [8]byte
	_, err := io.ReadFull(r, data[8-size:])
	return binary.BigEndian.Uint64(data[:]), err
}

func readChatValue(r *bytes.Reader, kind byte, depth int) (any, error) {
	if depth > 24 {
		return nil, fmt.Errorf("chat event nesting exceeds limit")
	}
	switch kind {
	case 2, 3:
		return chatUint(r, 1)
	case 6:
		return chatUint(r, 2)
	case 8:
		return chatUint(r, 4)
	case 4, 10:
		return chatUint(r, 8)
	case 11:
		n, err := chatUint(r, 4)
		if err != nil || n > uint64(r.Len()) {
			return nil, io.ErrUnexpectedEOF
		}
		data := make([]byte, int(n))
		_, err = io.ReadFull(r, data)
		return data, err
	case 12:
		fields := chatStruct{}
		for {
			fieldKind, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			if fieldKind == 0 {
				return fields, nil
			}
			id, err := chatUint(r, 2)
			if err != nil {
				return nil, err
			}
			if _, exists := fields[uint16(id)]; exists {
				return nil, fmt.Errorf("duplicate Chat field %d", id)
			}
			value, err := readChatValue(r, fieldKind, depth+1)
			if err != nil {
				return nil, err
			}
			fields[uint16(id)] = value
		}
	case 13, 14, 15:
		first, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		second := byte(0)
		if kind == 13 {
			second, err = r.ReadByte()
			if err != nil {
				return nil, err
			}
		}
		count, err := chatUint(r, 4)
		if err != nil || count > 10000 || count > uint64(r.Len()) {
			return nil, fmt.Errorf("invalid Chat collection size")
		}
		for range count {
			if _, err := readChatValue(r, first, depth+1); err != nil {
				return nil, err
			}
			if kind == 13 {
				if _, err := readChatValue(r, second, depth+1); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported Chat field type %d", kind)
	}
}

func (s chatStruct) text(id uint16) string {
	data, _ := s[id].([]byte)
	return string(data)
}

func (s chatStruct) child(id uint16) chatStruct {
	child, _ := s[id].(chatStruct)
	return child
}

type ChatMessage struct {
	ID                string `json:"id"`
	SequenceID        string `json:"sequence_id"`
	SenderID          string `json:"sender_id"`
	ConversationID    string `json:"conversation_id"`
	Text              string `json:"text,omitempty"`
	Encrypted         bool   `json:"encrypted"`
	conversationToken string
}

func decodeChatMessage(encoded string) (ChatMessage, bool, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ChatMessage{}, false, fmt.Errorf("decode Chat event base64: %w", err)
	}
	event, err := decodeChatStruct(data)
	if err != nil {
		return ChatMessage{}, false, err
	}
	create := event.child(7).child(1)
	if create == nil {
		return ChatMessage{Encrypted: event.child(7).child(3) != nil}, false, nil
	}
	m := ChatMessage{ID: event.text(2), SequenceID: event.text(1), SenderID: event.text(3), ConversationID: event.text(4), conversationToken: event.text(5)}
	if m.ID == "" || m.SequenceID == "" || m.SenderID == "" || m.ConversationID == "" {
		return ChatMessage{}, false, fmt.Errorf("chat message is missing its identity")
	}
	_, hasKey := create[101]
	version, hasVersion := create[112]
	if hasKey || (hasVersion && version != uint64(0)) {
		m.Encrypted = true
		return m, true, nil
	}
	contents, ok := create[100].([]byte)
	if !ok {
		return ChatMessage{}, false, fmt.Errorf("plain Chat message has no contents")
	}
	holder, err := decodeChatStruct(contents)
	if err != nil {
		return ChatMessage{}, false, err
	}
	m.Text = holder.child(1).child(1).text(1)
	if !utf8.ValidString(m.Text) {
		return ChatMessage{}, false, fmt.Errorf("chat message text is not UTF-8")
	}
	return m, true, nil
}
