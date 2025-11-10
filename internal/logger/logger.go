package logger

import (
	"io"
	"log"
	"os"
)

// Logger wraps standard log with debug flag
type Logger struct {
	debug bool
	*log.Logger
}

// New creates a new logger
func New(debug bool) *Logger {
	var writer io.Writer = io.Discard
	if debug {
		writer = os.Stderr
	}
	return &Logger{
		debug:  debug,
		Logger: log.New(writer, "", log.LstdFlags),
	}
}

// Printf logs if debug is enabled
func (l *Logger) Printf(format string, v ...interface{}) {
	if l.debug {
		l.Logger.Printf(format, v...)
	}
}

// Print logs if debug is enabled
func (l *Logger) Print(v ...interface{}) {
	if l.debug {
		l.Logger.Print(v...)
	}
}

// Println logs if debug is enabled
func (l *Logger) Println(v ...interface{}) {
	if l.debug {
		l.Logger.Println(v...)
	}
}

// Fatalf always logs (fatal errors)
func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.Logger.Fatalf(format, v...)
}

