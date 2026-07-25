package types

type ContextKey string

const (
	RequestIDContextKey ContextKey = "RequestID"
	LoggerContextKey    ContextKey = "logger"
)

func (c ContextKey) String() string {
	return string(c)
}
