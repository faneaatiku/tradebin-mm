package dto

import (
	"time"

	"cosmossdk.io/math"
)

const (
	CarouselType = "carousel"
	SpreadType   = "spread"

	defaultType = CarouselType
)

type VolumeStrategy struct {
	MinVolume       *math.Int
	MaxVolume       *math.Int
	RemainingVolume *math.Int
	OrderType       string
	LastRun         *time.Time
	ExtraMinVolume  *math.Int
	ExtraMaxVolume  *math.Int
	TradesCount     int64
	Type            string
}

func (v *VolumeStrategy) IncrementTradesCount() {
	v.TradesCount++
}

func (v *VolumeStrategy) GetTradesCount() int64 {
	return v.TradesCount
}

func (v *VolumeStrategy) SetRemainingAmount(amount *math.Int) {
	v.RemainingVolume = amount
	if !v.RemainingVolume.IsPositive() {
		zero := math.ZeroInt()
		v.RemainingVolume = &zero
	}
}

func (v *VolumeStrategy) LastRunAt() *time.Time {
	return v.LastRun
}

func (v *VolumeStrategy) SetLastRunAt(time *time.Time) {
	v.LastRun = time
}

func (v *VolumeStrategy) GetMinAmount() *math.Int {
	return v.MinVolume
}

func (v *VolumeStrategy) GetMaxAmount() *math.Int {
	return v.MaxVolume
}

func (v *VolumeStrategy) GetRemainingAmount() *math.Int {
	return v.RemainingVolume
}

func (v *VolumeStrategy) GetOrderType() string {
	return v.OrderType
}

func (v *VolumeStrategy) GetExtraMinVolume() *math.Int {
	return v.ExtraMinVolume
}

func (v *VolumeStrategy) GetExtraMaxVolume() *math.Int {
	return v.ExtraMaxVolume
}

func (v *VolumeStrategy) GetType() string {
	if v.Type == "" {
		return defaultType
	}

	if v.Type != CarouselType && v.Type != SpreadType {
		return defaultType
	}

	return v.Type
}
