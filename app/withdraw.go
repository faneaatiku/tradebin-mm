package app

import (
	"fmt"
	"tradebin-mm/app/internal"

	basev1beta1 "cosmossdk.io/api/cosmos/base/v1beta1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

type addressBalanceProvider interface {
	GetAddressBalances(address string) ([]*basev1beta1.Coin, error)
}

type sender interface {
	Send(from, to sdk.AccAddress, coins sdk.Coins) error
}

type Withdraw struct {
	balanceProvider addressBalanceProvider
	addrProvider    addressProvider
	l               logrus.FieldLogger

	sender sender
}

func NewWithdrawAction(l logrus.FieldLogger, b addressBalanceProvider, a addressProvider, s sender) (*Withdraw, error) {
	if l == nil || b == nil || a == nil || s == nil {
		return nil, internal.NewInvalidDependenciesErr("NewWithdrawAction")
	}

	return &Withdraw{
		balanceProvider: b,
		addrProvider:    a,
		l:               l.WithField("service", "WithdrawAction"),
		sender:          s,
	}, nil
}

func (w *Withdraw) WithdrawAll(destAddr sdk.AccAddress) error {
	if destAddr.Empty() {
		return fmt.Errorf("destination address is required")
	}

	balance, err := w.balanceProvider.GetAddressBalances(w.addrProvider.GetAddress().String())
	if err != nil {
		return fmt.Errorf("failed to get address balances: %v", err)
	}

	if balance == nil {
		return fmt.Errorf("failed to get address balances")
	}

	coins := sdk.NewCoins()
	for _, b := range balance {
		amtInt, _ := sdk.NewIntFromString(b.Amount)
		if amtInt.IsZero() {
			continue
		}

		if b.Denom == "ubze" {
			amtInt = amtInt.SubRaw(20_000)
			if amtInt.IsZero() {
				w.l.Infof("skipping ubze withdrawal because it is too small")
				continue
			}
		}

		c := sdk.NewCoin(b.Denom, amtInt)
		coins = coins.Add(c)
	}

	w.l.WithField("coins", coins.String()).Infof("created coins slice")

	return w.sender.Send(w.addrProvider.GetAddress(), destAddr, coins)
}
