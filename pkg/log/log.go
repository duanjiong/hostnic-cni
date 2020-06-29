package log

import (
	"flag"
	log "github.com/sirupsen/logrus"
)

type LogOptions struct {
	Level int
}

func NewLogOptions() *LogOptions {
	return &LogOptions{
		Level: int(log.InfoLevel),
	}
}

func (opt *LogOptions) AddFlags() {
	flag.IntVar(&opt.Level, "level", int(log.InfoLevel), "set log level")
}

func Setup(opt *LogOptions) {
	log.SetLevel(log.Level(opt.Level))
	log.SetReportCaller(false)
}