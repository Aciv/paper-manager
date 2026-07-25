package errors

import (
	"fmt"
	"net/http"
)

type ErrorCode int

const (
	// Common error codes (1000-1999)
	ErrBadRequest         ErrorCode = 1000
	ErrUnauthorized       ErrorCode = 1001
	ErrForbidden          ErrorCode = 1002
	ErrNotFound           ErrorCode = 1003
	ErrMethodNotAllowed   ErrorCode = 1004
	ErrConflict           ErrorCode = 1005
	ErrTooManyRequests    ErrorCode = 1006
	ErrInternalServer     ErrorCode = 1007
	ErrServiceUnavailable ErrorCode = 1008
	ErrTimeout            ErrorCode = 1009
	ErrValidation         ErrorCode = 1010
	ErrPayloadTooLarge    ErrorCode = 1011
	ErrUnsupportedMedia   ErrorCode = 1012
)

// ServerError defines the application error structure
type ServerError struct {
	Code     ErrorCode `json:"code"`
	Message  string    `json:"message"`
	Details  any       `json:"details,omitempty"`
	HTTPCode int       `json:"-"`
}

func IsServerError(err error) (*ServerError, bool) {
	serverErr, ok := err.(*ServerError)
	return serverErr, ok
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("error code: %d, error message: %s", e.Code, e.Message)
}

// WithDetails adds error details
func (e *ServerError) WithDetails(details any) *ServerError {
	e.Details = details
	return e
}

// NewBadRequestError creates a bad request error
func NewBadRequestError(message string) *ServerError {
	return &ServerError{
		Code:     ErrBadRequest,
		Message:  message,
		HTTPCode: http.StatusBadRequest,
	}
}

// NewUnauthorizedError creates an unauthorized error
func NewUnauthorizedError(message string) *ServerError {
	return &ServerError{
		Code:     ErrUnauthorized,
		Message:  message,
		HTTPCode: http.StatusUnauthorized,
	}
}

// NewForbiddenError creates a forbidden error
func NewForbiddenError(message string) *ServerError {
	return &ServerError{
		Code:     ErrForbidden,
		Message:  message,
		HTTPCode: http.StatusForbidden,
	}
}

// NewNotFoundError creates a not found error
func NewNotFoundError(message string) *ServerError {
	return &ServerError{
		Code:     ErrNotFound,
		Message:  message,
		HTTPCode: http.StatusNotFound,
	}
}

// NewConflictError creates a conflict error
func NewConflictError(message string) *ServerError {
	return &ServerError{
		Code:     ErrConflict,
		Message:  message,
		HTTPCode: http.StatusConflict,
	}
}

// NewTooManyRequestsError creates a 429 error, used by quota-style
// guards (e.g. per-user self-service tenant creation cap).
func NewTooManyRequestsError(message string) *ServerError {
	if message == "" {
		message = "too many requests"
	}
	return &ServerError{
		Code:     ErrTooManyRequests,
		Message:  message,
		HTTPCode: http.StatusTooManyRequests,
	}
}

// NewInternalServerError creates an internal server error
func NewInternalServerError(message string) *ServerError {
	if message == "" {
		message = "服务器内部错误"
	}
	return &ServerError{
		Code:     ErrInternalServer,
		Message:  message,
		HTTPCode: http.StatusInternalServerError,
	}
}

// NewServiceUnavailableError creates a service unavailable (503)
// error. Used for transient failures where the caller can retry.
func NewServiceUnavailableError(message string) *ServerError {
	if message == "" {
		message = "服务暂时不可用"
	}
	return &ServerError{
		Code:     ErrServiceUnavailable,
		Message:  message,
		HTTPCode: http.StatusServiceUnavailable,
	}
}

// NewPayloadTooLargeError creates a 413 error for requests exceeding a size limit.
func NewPayloadTooLargeError(message string) *ServerError {
	return &ServerError{
		Code:     ErrPayloadTooLarge,
		Message:  message,
		HTTPCode: http.StatusRequestEntityTooLarge,
	}
}

// NewUnsupportedMediaTypeError creates a 415 error for unsupported file types.
func NewUnsupportedMediaTypeError(message string) *ServerError {
	return &ServerError{
		Code:     ErrUnsupportedMedia,
		Message:  message,
		HTTPCode: http.StatusUnsupportedMediaType,
	}
}
