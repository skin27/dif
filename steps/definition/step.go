// Package definition defines the contract every step implements.
package definition

import "dif/message"

// Step receives a Message and returns a Message.
type Step interface {
	Execute(*message.Message) (*message.Message, error)
}
