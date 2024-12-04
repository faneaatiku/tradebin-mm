package dto

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"time"
)

type VolumeStrategy struct {
	MinVolume       *sdk.Int
	MaxVolume       *sdk.Int
	RemainingVolume *sdk.Int
	OrderType       string
	LastRun         *time.Time
}

func (v *VolumeStrategy) SetRemainingAmount(amount *sdk.Int) {
	v.RemainingVolume = amount
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
