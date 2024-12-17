package dto

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"time"
)

const (
	CarouselType = "carousel"
	SpreadType   = "spread"

	defaultType = CarouselType
)

type VolumeStrategy struct {
	MinVolume       *sdk.Int
	MaxVolume       *sdk.Int
	RemainingVolume *sdk.Int
	OrderType       string
	LastRun         *time.Time
	ExtraMinVolume  *sdk.Int
	ExtraMaxVolume  *sdk.Int
	TradesCount     int64
	Type            string
}

func (v *VolumeStrategy) IncrementTradesCount() {
	v.TradesCount++
}

func (v *VolumeStrategy) GetTradesCount() int64 {
	return v.TradesCount
}

func (v *VolumeStrategy) SetRemainingAmount(amount *sdk.Int) {
	v.RemainingVolume = amount
	if !v.RemainingVolume.IsPositive() {
		zero := sdk.ZeroInt()
		v.RemainingVolume = &zero
	}
}

func (v *VolumeStrategy) LastRunAt() *time.Time {
	return v.LastRun
}

func (v *VolumeStrategy) SetLastRunAt(time *time.Time) {
	v.LastRun = time
}

func (v *VolumeStrategy) GetMinAmount() *sdk.Int {
	return v.MinVolume
}

func (v *VolumeStrategy) GetMaxAmount() *sdk.Int {
	return v.MaxVolume
}

func (v *VolumeStrategy) GetRemainingAmount() *sdk.Int {
	return v.RemainingVolume
}

func (v *VolumeStrategy) GetOrderType() string {
	return v.OrderType
}

func (v *VolumeStrategy) GetExtraMinVolume() *sdk.Int {
	return v.ExtraMinVolume
}

func (v *VolumeStrategy) GetExtraMaxVolume() *sdk.Int {
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
