package main

import (
	"github.com/sirupsen/logrus"
	"tradebin-mm/config"
)

func NewLogger(config config.Logging) (logrus.FieldLogger, error) {
	logger := logrus.New()

	parsedLogLevel, err := logrus.ParseLevel(config.Level)
	if err != nil {
		logger.Fatal("error on parsing logging level: %s", err)
	}

	logger.SetLevel(parsedLogLevel)

	return logger, nil
}
