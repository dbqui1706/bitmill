package logger

import (
	"fmt"
	"log"
	"time"
)

type Logger struct{}

func New() *Logger {
	return &Logger{}
}

func (l *Logger) Info(format string, v ...interface{}) {
	t := time.Now().Format("2006-01-02 15:04:05")
	log.Printf("[INFO]  %s | %s\n", t, fmt.Sprintf(format, v...))
}

func (l *Logger) Error(format string, v ...interface{}) {
	t := time.Now().Format("2006-01-02 15:04:05")
	log.Printf("[ERROR] %s | %s\n", t, fmt.Sprintf(format, v...))
}

func (l *Logger) Warn(format string, v ...interface{}) {
	t := time.Now().Format("2006-01-02 15:04:05")
	log.Printf("[WARN]  %s | %s\n", t, fmt.Sprintf(format, v...))
}
