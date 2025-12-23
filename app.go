package main

import (
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	restartInterval = 30
)

type marketFiller interface {
	FillOrderBook() (time.Duration, error)
}

type volumeMaker interface {
	MakeVolume() (time.Duration, error)
}

type App struct {
	l logrus.FieldLogger
	v volumeMaker
	f marketFiller
}

func NewApp(l logrus.FieldLogger, v volumeMaker, f marketFiller) (*App, error) {
	if l == nil || v == nil || f == nil {
		return nil, fmt.Errorf("logger, volume maker and market filler must be provided")
	}

	return &App{
		l: l.WithField("service", "APP"),
		v: v,
		f: f,
	}, nil
}

func (a *App) Start() {
	a.l.Info("starting app...")
	a.execute()
}

func (a *App) execute() {
	a.l.Debug("executing app...")

	orderBookHoldback, err := a.f.FillOrderBook()
	if err != nil {
		a.l.WithError(err).Errorf("error encountered while filling order book")

		a.afterExecute(time.Duration(restartInterval) * time.Second)

		return
	}

	volumeHoldBack, err := a.v.MakeVolume()
	if err != nil {
		a.l.WithError(err).Errorf("error encountered while making volume")
		a.afterExecute(time.Duration(restartInterval) * time.Second)

		return
	}

	a.l.Infof("holdbacks: order book: %s, volume: %s", orderBookHoldback, volumeHoldBack)

	//we restart the app after the holdback is finished
	a.afterExecute(min(orderBookHoldback, volumeHoldBack))
}

func (a *App) afterExecute(restartDelay time.Duration) {
	a.l.Info("app finished")
	time.AfterFunc(restartDelay, func() {
		a.execute()
	})
	a.l.Infof("restarting in %d seconds", restartDelay/time.Second)
}
