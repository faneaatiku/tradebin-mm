package service

import (
	"tradebin-mm/app/internal"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/sirupsen/logrus"
)

type Sender struct {
	b *Broadcaster
	l logrus.FieldLogger
}

func NewSender(b *Broadcaster, l logrus.FieldLogger) (*Sender, error) {
	if b == nil || l == nil {
		return nil, internal.NewInvalidDependenciesErr("NewSender")
	}

	return &Sender{
		b: b,
		l: l,
	}, nil
}

func (s *Sender) Send(from, to sdk.AccAddress, coins sdk.Coins) error {
	msg := types.NewMsgSend(from, to, coins)

	m, err := anyToSdkMsgs([]interface{}{msg})
	if err != nil {
		return err
	}
	
	return s.b.BroadcastBlock(m)
}
