package errors

import (
	stderrors "errors"
	"fmt"
)

type BusinessError struct {
	StatusCode int
	Message    string
	Data       any
	Err        error
}

func New(statusCode int, message string) *BusinessError {
	return &BusinessError{StatusCode: statusCode, Message: message}
}

func NewWithData(statusCode int, message string, data any) *BusinessError {
	return &BusinessError{StatusCode: statusCode, Message: message, Data: data}
}

func Wrap(statusCode int, message string, err error) *BusinessError {
	return &BusinessError{StatusCode: statusCode, Message: message, Err: err}
}

func (e *BusinessError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (e *BusinessError) Unwrap() error {
	return e.Err
}

func AsBusinessError(err error) (*BusinessError, bool) {
	var target *BusinessError
	if stderrors.As(err, &target) {
		return target, true
	}
	return nil, false
}
